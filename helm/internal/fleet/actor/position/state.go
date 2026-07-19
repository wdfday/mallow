// Package position defines the position state machine for a hand's legs.
//
// Phase transitions per leg:
//
//	Idle ──order_place──▶ Entering ──order_filled──▶ Open ──order_place(close)──▶ Exiting ──order_filled──▶ Idle
//	                           │                       │                              │
//	                     order_cancelled          order_place(add)            order_canceled
//	                           │                   (pyramid only)                     │
//	                          Idle                    ▼                              Open
//	                                               Adding ──order_filled──▶ Open
//	                                                       ──order_canceled──▶ Open
//
// All three "order_place" transitions above are the SAME poslog event kind (KindOrderPlace,
// "order_place" — written pre-flight, before the exchange REST call), differentiated only by
// the IsClose/IsPyramidAdd payload flags (see LegState.applyOrderPlace). KindOrderPlaced
// ("order_placed", past tense) is a separate, later event — it echoes the real exchange
// order_id once the REST call confirms, and does NOT drive these transitions in the normal
// flow (only as a backward-compat fallback when order_place was skipped — legacy events).
//
// Non-pyramid: each signal opens an independent leg (bounded by MaxUnits).
// Pyramid:     all adds merge into Legs[0]; avg_entry recalculated; SL/TP from latest signal.
//
// State is fully reconstructed by replaying poslog.Events — no SQL needed.
package position

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/module/hand/domain"
)

// Phase represents where in the lifecycle a position leg is.
type Phase string

const (
	// PhaseIdle: no open position, no pending order.
	PhaseIdle Phase = "idle"
	// PhaseEntering: entry order placed, waiting for fill.
	PhaseEntering Phase = "entering"
	// PhaseAdding: pyramid add order placed on an open leg, waiting for fill.
	PhaseAdding Phase = "adding"
	// PhaseOpen: position live at exchange.
	PhaseOpen Phase = "open"
	// PhaseExiting: close order placed, waiting for fill.
	PhaseExiting Phase = "exiting"
)

// LegState is the state machine for a single position leg (one PositionID).
//
// Pyramid mode: one LegState per hand; Qty/EntryPrice accumulate on each add.
// Non-pyramid:  one LegState per entry; each has its own SL/TP.
type LegState struct {
	Phase      Phase
	PositionID string // = opening order_id; stable across pyramid adds

	Symbol string
	Side   string // "buy" | "sell"

	PendingOrderID string

	EntryPrice      decimal.Decimal // avg_entry (pyramid: recomputed on each add fill)
	Qty             decimal.Decimal // total qty  (pyramid: accumulated)
	DeployedCapital decimal.Decimal // total quote cost across all entry fills: sum(net_qty×price + entry_fee_quote)
	StopLoss        decimal.Decimal // zero = not set
	TakeProfit      decimal.Decimal // zero = not set

	Price     string
	OrderType string

	OpenedAt time.Time

	// entryCount is the number of fills that have opened or added to this leg.
	// Initial entry = 1; each pyramid add increments by 1.
	// Used by HandPositions.EntryCount() to enforce MaxUnits in pyramid mode.
	entryCount int

	// pendingAdd* holds SL/TP from a pending pyramid add's order_placed.
	// Committed to the leg on fill; discarded on cancel.
	pendingAddSL decimal.Decimal
	pendingAddTP decimal.Decimal

	// ExchangeOrderIDs holds the exchange-side SL/TP bracket order IDs placed after an
	// entry fill. Populated via KindBracketPlaced poslog events so they survive restarts.
	// Used by cancelExitOrders to cancel the OCO sibling on position close.
	ExchangeOrderIDs []string
}

// IsActive reports whether the leg has an open position or a pending order.
func (l *LegState) IsActive() bool { return l.Phase != PhaseIdle }

// EntryCount returns the number of entry fills that have contributed to this leg
// (1 for a plain entry, 1+N for pyramid adds). Exposed for audit callers outside
// the position package (e.g. appendOrphanTradeRecord).
func (l *LegState) EntryCount() int { return l.entryCount }

// HasExitManagement reports whether the leg has any exit tracking:
// either absolute SL/TP prices or live exchange OCO order IDs.
func (l *LegState) HasExitManagement() bool {
	return l.StopLoss.IsPositive() || l.TakeProfit.IsPositive() || len(l.ExchangeOrderIDs) > 0
}

// HasPendingOrder reports whether the leg is waiting for an exchange fill.
func (l *LegState) HasPendingOrder() bool {
	return l.Phase == PhaseEntering || l.Phase == PhaseAdding || l.Phase == PhaseExiting
}

// Apply advances the leg's state machine by one poslog event.
// Returns an error if the transition is invalid for the current phase.
func (l *LegState) Apply(e poslog.Event) error {
	switch e.Kind {
	case poslog.KindOrderPlace:
		return l.applyOrderPlace(e)
	case poslog.KindOrderPlaced:
		return l.applyOrderPlaced(e)
	case poslog.KindOrderFilled:
		return l.applyOrderFilled(e)
	case poslog.KindOrderCancelled:
		return l.applyOrderCancelled(e)
	case poslog.KindSLUpdated:
		return l.applySLUpdated(e)
	default:
		return fmt.Errorf("unhandled event kind %q in LegState.Apply", e.Kind)
	}
}

func (l *LegState) applyOrderPlace(e poslog.Event) error {
	var p poslog.OrderPlacePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	sl, _ := decimal.NewFromString(p.StopLoss)
	tp, _ := decimal.NewFromString(p.TakeProfit)

	l.Price = p.Price
	l.OrderType = p.OrderType

	switch {
	case p.IsClose:
		if l.Phase != PhaseOpen {
			return fmt.Errorf("order_place(close) invalid in phase %q", l.Phase)
		}
		l.PendingOrderID = p.ClientOrderID
		l.Phase = PhaseExiting

	case p.IsPyramidAdd:
		if l.Phase != PhaseOpen {
			return fmt.Errorf("order_place(pyramid_add) invalid in phase %q", l.Phase)
		}
		l.PendingOrderID = p.ClientOrderID
		l.pendingAddSL = sl
		l.pendingAddTP = tp
		l.Phase = PhaseAdding

	default:
		// Opening entry.
		if l.Phase != PhaseIdle {
			return fmt.Errorf("order_place(open) invalid in phase %q", l.Phase)
		}
		l.PendingOrderID = p.ClientOrderID
		l.Symbol = p.Symbol
		l.Side = p.Side
		l.StopLoss = sl
		l.TakeProfit = tp
		l.Phase = PhaseEntering
	}
	return nil
}

func (l *LegState) applyOrderPlaced(e poslog.Event) error {
	var p poslog.OrderPlacedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}

	l.Price = p.Price
	l.OrderType = p.OrderType

	// Backward-compatibility: if order_place was skipped (e.g. legacy events),
	// we still support transitioning from Idle / Open.
	if l.Phase == PhaseIdle || l.Phase == PhaseOpen {
		sl, _ := decimal.NewFromString(p.StopLoss)
		tp, _ := decimal.NewFromString(p.TakeProfit)
		switch {
		case p.IsClose:
			l.PendingOrderID = p.OrderID
			l.Phase = PhaseExiting
		case p.IsPyramidAdd:
			l.PendingOrderID = p.OrderID
			l.pendingAddSL = sl
			l.pendingAddTP = tp
			l.Phase = PhaseAdding
		default:
			l.PendingOrderID = p.OrderID
			l.Symbol = p.Symbol
			l.Side = p.Side
			l.StopLoss = sl
			l.TakeProfit = tp
			l.Phase = PhaseEntering
		}
		return nil
	}

	// Normal path: we were already in Entering / Adding / Exiting from order_place.
	// Verify client_order_id matches our temporary l.PendingOrderID.
	if p.ClientOrderID != l.PendingOrderID {
		return fmt.Errorf("order_placed client_order_id mismatch: got %q want %q", p.ClientOrderID, l.PendingOrderID)
	}
	// Update PendingOrderID to the actual exchange order ID.
	l.PendingOrderID = p.OrderID
	return nil
}

func (l *LegState) applyOrderFilled(e poslog.Event) error {
	if !l.HasPendingOrder() {
		return fmt.Errorf("order_filled invalid in phase %q", l.Phase)
	}
	var p poslog.OrderFilledPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.OrderID != l.PendingOrderID {
		return fmt.Errorf("order_id mismatch: got %q want %q", p.OrderID, l.PendingOrderID)
	}

	price, _ := decimal.NewFromString(p.FillPrice)
	qty, _ := decimal.NewFromString(p.FillQty)

	switch l.Phase {
	case PhaseEntering:
		l.EntryPrice = price
		l.Qty = qty
		l.OpenedAt = e.At
		l.entryCount++
		l.DeployedCapital, _ = decimal.NewFromString(p.DeployedCapital)
		l.Phase = PhaseOpen

	case PhaseAdding:
		// Recalculate avg_entry and accumulate qty.
		totalQty := l.Qty.Add(qty)
		l.EntryPrice = l.Qty.Mul(l.EntryPrice).Add(qty.Mul(price)).Div(totalQty)
		l.Qty = totalQty
		// Commit pending SL/TP from the add signal.
		l.StopLoss = l.pendingAddSL
		l.TakeProfit = l.pendingAddTP
		l.pendingAddSL = decimal.Zero
		l.pendingAddTP = decimal.Zero
		l.entryCount++
		if add, _ := decimal.NewFromString(p.DeployedCapital); add.IsPositive() {
			l.DeployedCapital = l.DeployedCapital.Add(add)
		}
		l.Phase = PhaseOpen

	case PhaseExiting:
		// Close fill: PnL is computed by HandPositions.Apply (it snapshots state beforehand).
		// Reset leg to idle.
		l.reset()
		return nil
	}

	l.PendingOrderID = ""
	return nil
}

func (l *LegState) applyOrderCancelled(_ poslog.Event) error {
	if !l.HasPendingOrder() {
		return fmt.Errorf("order_cancelled invalid in phase %q", l.Phase)
	}
	switch l.Phase {
	case PhaseEntering:
		l.reset()
	case PhaseAdding:
		// Cancelled pyramid add → stay open; discard pending levels.
		l.pendingAddSL = decimal.Zero
		l.pendingAddTP = decimal.Zero
		l.PendingOrderID = ""
		l.Phase = PhaseOpen
	case PhaseExiting:
		// Cancelled close → position remains open.
		l.PendingOrderID = ""
		l.Phase = PhaseOpen
	}
	return nil
}

func (l *LegState) applySLUpdated(e poslog.Event) error {
	if l.Phase != PhaseOpen {
		return fmt.Errorf("sl_updated invalid in phase %q", l.Phase)
	}
	var p poslog.SLUpdatedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	if p.NewSL != "" {
		if sl, err := decimal.NewFromString(p.NewSL); err == nil {
			l.StopLoss = sl
		}
	}
	if p.NewTP != "" {
		if tp, err := decimal.NewFromString(p.NewTP); err == nil {
			l.TakeProfit = tp
		}
	}
	return nil

}

func (l *LegState) reset() {
	l.Phase = PhaseIdle
	l.Symbol = ""
	l.Side = ""
	l.PendingOrderID = ""
	l.EntryPrice = decimal.Zero
	l.Qty = decimal.Zero
	l.DeployedCapital = decimal.Zero
	l.StopLoss = decimal.Zero
	l.TakeProfit = decimal.Zero
	l.OpenedAt = time.Time{}
	l.entryCount = 0
	l.pendingAddSL = decimal.Zero
	l.pendingAddTP = decimal.Zero
}

// ── HandPositions ─────────────────────────────────────────────────────────────

// HandPositions is the complete position state for one hand across all its legs.
//
// Pyramid (Pyramid=true):   at most 1 active leg; qty/avg_entry accumulate on each add.
// Non-pyramid (Pyramid=false): up to MaxUnits independent active legs; each has its own SL/TP.
type HandPositions struct {
	Pyramid  bool
	MaxUnits int

	// legs maps PositionID → leg for active legs only.
	// Closed legs are evicted immediately when they transition to PhaseIdle so the map
	// stays bounded regardless of how many trades the hand executes over its lifetime.
	// Dedup for duplicate position_closed events is handled by the !exists early-return
	// in applyPositionClosed — a missing leg and an idle leg are equivalent there.
	legs        map[string]*LegState
	RealizedPnL decimal.Decimal // accumulated from all closed legs
}

// NewHandPositions creates an empty HandPositions.
func NewHandPositions(pyramid bool, maxUnits int) *HandPositions {
	if maxUnits <= 0 {
		maxUnits = 1
	}
	return &HandPositions{
		Pyramid:  pyramid,
		MaxUnits: maxUnits,
		legs:     make(map[string]*LegState),
	}
}

// ActiveCount returns the number of legs currently in a non-idle phase.
func (h *HandPositions) ActiveCount() int {
	n := 0
	for _, l := range h.legs {
		if l.IsActive() {
			n++
		}
	}
	return n
}

// EntryCount returns the number of filled entries against the MaxUnits cap.
//
// Non-pyramid: equals ActiveCount() — each signal opens an independent leg.
// Pyramid:     equals the entryCount of the active leg — each add increments it.
//
// A return value of 0 means the hand is flat and ready for a fresh entry.
func (h *HandPositions) EntryCount() int {
	if !h.Pyramid {
		return h.ActiveCount()
	}
	leg := h.PrimaryLeg()
	if leg == nil {
		return 0
	}
	return leg.entryCount
}

// ActiveLegs returns a slice of all currently active legs, ordered by PositionID.
func (h *HandPositions) ActiveLegs() []*LegState {
	out := make([]*LegState, 0, len(h.legs))
	for _, l := range h.legs {
		if l.IsActive() {
			out = append(out, l)
		}
	}
	return out
}

// PrimaryLeg returns the oldest active leg, or nil if idle.
// For pyramid mode this is always Legs[0]. For non-pyramid it is the first-opened leg.
func (h *HandPositions) PrimaryLeg() *LegState {
	var oldest *LegState
	for _, l := range h.legs {
		if !l.IsActive() {
			continue
		}
		if oldest == nil || l.OpenedAt.Before(oldest.OpenedAt) {
			oldest = l
		}
	}
	return oldest
}

// IsFlat reports whether the hand has no open exposure and no pending order.
func (h *HandPositions) IsFlat() bool { return h.ActiveCount() == 0 }

// LegPhase returns the Phase of the leg with the given PositionID.
// Returns PhaseIdle if the leg does not exist or has been closed.
func (h *HandPositions) LegPhase(positionID string) Phase {
	if leg, ok := h.legs[positionID]; ok {
		return leg.Phase
	}
	return PhaseIdle
}

// LegEntryPrice returns the avg entry price of the leg with the given PositionID.
// Returns zero if the leg does not exist or has not yet been entered.
func (h *HandPositions) LegEntryPrice(positionID string) decimal.Decimal {
	if leg, ok := h.legs[positionID]; ok {
		return leg.EntryPrice
	}
	return decimal.Zero
}

// LegSnapshot returns a snapshot of a leg's trade-relevant fields.
// ok=false when the positionID is not tracked.
func (h *HandPositions) LegSnapshot(positionID string) (LegSnapshot, bool) {
	leg, ok := h.legs[positionID]
	if !ok {
		return LegSnapshot{}, false
	}
	return LegSnapshot{
		Symbol:          leg.Symbol,
		Side:            leg.Side,
		Qty:             leg.Qty,
		EntryPrice:      leg.EntryPrice,
		DeployedCapital: leg.DeployedCapital,
		StopLoss:        leg.StopLoss,
		TakeProfit:      leg.TakeProfit,
		OpenedAt:        leg.OpenedAt,
		NEntries:        leg.entryCount,
	}, true
}

// LegSnapshot carries the trade-relevant fields of a leg for poslog enrichment.
// Captured at the moment a closing event is recorded so the resulting trade
// record can stand alone (no need to replay entry events).
type LegSnapshot struct {
	Symbol          string
	Side            string
	Qty             decimal.Decimal
	EntryPrice      decimal.Decimal
	DeployedCapital decimal.Decimal // total quote cost across all entry fills
	StopLoss        decimal.Decimal // SL price active at time of close (zero = none)
	TakeProfit      decimal.Decimal // TP price active at time of close (zero = none)
	OpenedAt        time.Time
	NEntries        int // number of entry fills (1 for non-pyramid; 1 + adds for pyramid)
}

// Apply dispatches a poslog event to the appropriate leg, creating it if needed.
//
// PnL accounting:
//   - Close fill (order_filled on PhaseExiting): PnL computed from fill vs avg_entry.
//   - External close (position_closed on non-idle leg with no prior fill): PnL from payload.
//   - position_closed on already-idle leg: no-op (already handled via order_filled).
func (h *HandPositions) Apply(e poslog.Event) error {
	// position_closed is handled directly — it may arrive after order_filled has already
	// reset the leg (in which case it is a no-op), or it may be the only close signal
	// (external close — liquidation, manual) with authoritative PnL in the payload.
	if e.Kind == poslog.KindPositionClosed {
		return h.applyPositionClosed(e)
	}

	// position_orphaned: hand released this leg without closing.
	// Remove it so reconciler never restores it to this hand.
	if e.Kind == poslog.KindPositionOrphaned {
		delete(h.legs, e.PositionID)
		return nil
	}

	// bracket_placed: persist exchange-side SL/TP order IDs so they survive restarts.
	if e.Kind == poslog.KindBracketPlaced {
		leg, ok := h.legs[e.PositionID]
		if ok {
			var p poslog.BracketPlacedPayload
			if err := json.Unmarshal(e.Payload, &p); err == nil {
				leg.ExchangeOrderIDs = p.OrderIDs
			}
		}
		return nil
	}

	leg, exists := h.legs[e.PositionID]
	if !exists {
		// Leg can be created by a new entry order_place (new path) or order_placed (legacy path).
		if e.Kind != poslog.KindOrderPlace && e.Kind != poslog.KindOrderPlaced {
			return fmt.Errorf("no leg for position_id %q (event: %s)", e.PositionID, e.Kind)
		}
		var pIsPyramidAdd, pIsClose bool
		if e.Kind == poslog.KindOrderPlace {
			var p poslog.OrderPlacePayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			pIsPyramidAdd = p.IsPyramidAdd
			pIsClose = p.IsClose
		} else {
			var p poslog.OrderPlacedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				return err
			}
			pIsPyramidAdd = p.IsPyramidAdd
			pIsClose = p.IsClose
		}
		if pIsPyramidAdd || pIsClose {
			return fmt.Errorf("no active leg for position_id %q but got %s with pyramid_add/close", e.PositionID, e.Kind)
		}
		leg = &LegState{PositionID: e.PositionID, Phase: PhaseIdle}
		h.legs[e.PositionID] = leg
	}

	// For a closing fill, snapshot what we need for PnL before Apply resets the leg.
	var (
		snapPhase      = leg.Phase
		snapSide       = leg.Side
		snapEntryPrice = leg.EntryPrice
	)

	if err := leg.Apply(e); err != nil {
		return err
	}

	// Closing fill: leg transitioned Exiting → Idle; compute PnL and evict.
	if e.Kind == poslog.KindOrderFilled && snapPhase == PhaseExiting && leg.Phase == PhaseIdle {
		var p poslog.OrderFilledPayload
		if err := json.Unmarshal(e.Payload, &p); err == nil {
			fillPrice, _ := decimal.NewFromString(p.FillPrice)
			fillQty, _ := decimal.NewFromString(p.FillQty)
			h.RealizedPnL = h.RealizedPnL.Add(computePnL(snapSide, snapEntryPrice, fillPrice, fillQty))
		}
		// Leg is fully closed — evict to keep map bounded.
		delete(h.legs, e.PositionID)
		return nil
	}

	// Entry cancel: leg never opened — evict immediately.
	if e.Kind == poslog.KindOrderCancelled && snapPhase == PhaseEntering && leg.Phase == PhaseIdle {
		delete(h.legs, e.PositionID)
	}

	return nil
}

func (h *HandPositions) applyPositionClosed(e poslog.Event) error {
	leg, exists := h.legs[e.PositionID]
	if !exists || leg.Phase == PhaseIdle {
		// Already closed via order_filled (leg evicted), or never opened — no-op.
		return nil
	}
	// External close (bracket fill, liquidation, manual): use authoritative PnL from payload.
	var p poslog.PositionClosedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return err
	}
	pnl, _ := decimal.NewFromString(p.RealizedPnL)
	h.RealizedPnL = h.RealizedPnL.Add(pnl)
	// Evict — leg is fully closed.
	delete(h.legs, e.PositionID)
	return nil
}

func computePnL(side string, entryPrice, fillPrice, qty decimal.Decimal) decimal.Decimal {
	diff := fillPrice.Sub(entryPrice)
	if side == "sell" {
		diff = diff.Neg()
	}
	return diff.Mul(qty)
}

// ReplayHand reconstructs HandPositions by replaying all events for a hand.
// Events must be in ascending sequence order (as returned by poslog.Log.ReplayHand).
// Invalid events are skipped — replay is best-effort to survive log corruption.
func ReplayHand(events []poslog.Event, pyramid bool, maxUnits int) *HandPositions {
	h := NewHandPositions(pyramid, maxUnits)
	for _, e := range events {
		if err := h.Apply(e); err != nil {
			// TODO: log bad event in production; skip for now.
			_ = err
		}
	}
	return h
}

// ToPosition converts the current hand state to the canonical domain.Position value object.
// currentPrice is injected to compute UnrealizedPnL / MarketValue; zero skips market fields.
func (h *HandPositions) ToPosition(handID, helmID string, currentPrice decimal.Decimal) domain.Position {
	pos := domain.Position{
		HandID:      handID,
		HelmID:      helmID,
		RealizedPnL: h.RealizedPnL,
		Pyramid:     h.Pyramid,
	}

	// Phase priority for aggregate reporting: transitional > open > idle.
	phaseRank := func(p Phase) int {
		switch p {
		case PhaseEntering, PhaseAdding, PhaseExiting:
			return 2
		case PhaseOpen:
			return 1
		default:
			return 0
		}
	}
	bestRank := -1
	bestPhase := PhaseIdle

	for _, leg := range h.legs {
		if !leg.IsActive() {
			continue
		}
		pos.Symbol = leg.Symbol
		pos.Side = leg.Side

		dLeg := domain.Leg{
			PositionID: leg.PositionID,
			EntryPrice: leg.EntryPrice,
			Qty:        leg.Qty,
			StopLoss:   leg.StopLoss,
			TakeProfit: leg.TakeProfit,
			OpenedAt:   leg.OpenedAt,
		}
		pos.Legs = append(pos.Legs, dLeg)

		if pos.OpenedAt.IsZero() || leg.OpenedAt.Before(pos.OpenedAt) {
			pos.OpenedAt = leg.OpenedAt
		}
		if leg.OpenedAt.After(pos.LastEntryAt) {
			pos.LastEntryAt = leg.OpenedAt
		}

		if r := phaseRank(leg.Phase); r > bestRank {
			bestRank = r
			bestPhase = leg.Phase
		}
	}

	pos.Phase = string(bestPhase)

	// Aggregated qty and avg_entry across all active legs.
	var totalQtyTimesPrice decimal.Decimal
	for _, l := range pos.Legs {
		pos.TotalQty = pos.TotalQty.Add(l.Qty)
		totalQtyTimesPrice = totalQtyTimesPrice.Add(l.Qty.Mul(l.EntryPrice))
	}
	if !pos.TotalQty.IsZero() {
		pos.AvgEntryPrice = totalQtyTimesPrice.Div(pos.TotalQty)
	}

	// Pyramid: SL/TP from the single leg (= latest signal's levels).
	// Non-pyramid: SL/TP are per-leg; pos-level fields remain zero.
	if h.Pyramid && len(pos.Legs) == 1 {
		pos.StopLoss = pos.Legs[0].StopLoss
		pos.TakeProfit = pos.Legs[0].TakeProfit
	}

	if currentPrice.IsPositive() {
		return pos.WithPrice(currentPrice)
	}
	return pos
}
