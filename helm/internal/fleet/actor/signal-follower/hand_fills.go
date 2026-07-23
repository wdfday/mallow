package signalfollower

import (
	"context"
	"encoding/json"
	"fmt"
	"mallow/helm/internal/fleet/actor/clid"
	"mallow/helm/internal/fleet/actor/eventcode"
	"mallow/helm/internal/fleet/actor/signal-follower/position"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/infra/natsapi"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/safe"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// handleWsFill processes a fully-filled WsFillEvent received via the WS path.
func (h *Hand) handleWsFill(ctx context.Context, ev exchange.WsFillEvent) {
	h.mu.Lock()
	if _, seen := h.seenFills[ev.OrderID]; seen {
		// REST-immediate path already handled this fill — skip to avoid double-apply.
		h.mu.Unlock()
		return
	}
	h.seenFills[ev.OrderID] = time.Now()
	h.mu.Unlock()

	side := "buy"
	if ev.Side == exchange.Sell {
		side = "sell"
	}
	h.applyFill(ctx, ev.OrderID, ev.Symbol, side, ev.FilledQty, ev.FilledAvg, ev.Commission, "ws")
}

// applyFill is the single authority for all fill side effects:
// portfolio update, exit-level management, metrics, and poslog publishing.
// It is called from three paths:
//   - WS (handleWsFill): fast-path, fill arrives via broker WebSocket
//   - REST poll (pollOrders): 5s fallback when WS event was missed
//   - REST-immediate (handleSignal): broker confirmed fill in the PlaceOrder response // deprecated, rest-immediate is now ack only
func (h *Hand) applyFill(ctx context.Context, orderID, symbol, side string,
	qty, price, commission decimal.Decimal, source string) {

	h.metrics.ordersFilled.Add(1)

	// ── Pre-process partials ─────────────────────────────────────────────────
	h.mu.Lock()
	state := h.partialApplied[orderID]
	delete(h.partialApplied, orderID)
	h.mu.Unlock()

	cumulativeQty := qty.Add(state.Qty)
	cumulativeCommission := commission.Add(state.Commission)
	cumulativeAvgPrice := price
	if cumulativeQty.IsPositive() {
		cumulativeCost := qty.Mul(price).Add(state.Cost)
		cumulativeAvgPrice = cumulativeCost.Div(cumulativeQty)
	}

	// ── Detect fill type: entry vs exit ──────────────────────────────────────
	// tradeID and preFillPhase are captured here, before publishOrderFilled mutates
	// leg state, so they correctly reflect the leg's intent at fill time.
	h.mu.RLock()
	tradeID := h.pendingOrderPos[orderID]
	preFillPhase := h.pos.LegPhase(tradeID)
	isBracketExit := false
	// Scan all exitLevels entries (keyed by TradeID, not symbol) for the one whose
	// bracket owns this order ID — correctly attributes the fill to its exact leg
	// even when multiple independent legs share a symbol (non-pyramid, MaxUnits>1).
	for tid, lv := range h.exitLevels {
		if slices.Contains(lv.ExchangeOrderIDs, orderID) {
			isBracketExit = true
			tradeID = tid
			break
		}
	}
	h.mu.RUnlock()
	isClosingFill := preFillPhase == position.PhaseExiting || isBracketExit

	// ── Route to entry or exit handler ───────────────────────────────────────
	var resolvedEl exitLevel
	var offsetResolved bool
	var legQty, entryPrice, closePnL decimal.Decimal
	var closeSource string

	if isClosingFill {
		legQty, entryPrice, closePnL, closeSource = h.applyExitFill(
			ctx, symbol, orderID, side, source, tradeID, isBracketExit,
			cumulativeAvgPrice, cumulativeQty, cumulativeCommission,
		)
	} else {
		resolvedEl, offsetResolved = h.applyEntryFill(
			ctx, symbol, orderID, tradeID,
			cumulativeQty, cumulativeAvgPrice, cumulativeCommission,
		)
		h.metrics.mu.Lock()
		h.metrics.totalCommission = h.metrics.totalCommission.Add(cumulativeCommission)
		h.metrics.mu.Unlock()
	}

	// ── 4. Portfolio update (main exchange fill) ─────────────────────────────
	h.helm.ReportFill(helmdomain.FillReport{
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		OrderID:    orderID,
		Symbol:     symbol,
		Side:       side,
		Qty:        qty,
		Price:      price,
		Commission: commission,
		Timestamp:  time.Now().UTC(),
	})

	// ── 4.5. Dust reconciliation (spot closing fills only) ────────────────────
	// When a spot exit is truncated to the exchange's LOT_SIZE step, the remaining
	// sub-step qty (dust) cannot be sold at the exchange. The hand "sells" the
	// dust internally to helm at the current market price so that:
	//   1. Helm's portfolio is credited with the USDT value of the dust.
	//   2. dustPnL is added to closePnL so pnlPct in eventcode.CodePositionClosed is correct.
	//   3. dustLedger suppresses a false checkPositionDesync alarm for the
	//      tiny physical BTC residual that remains at the exchange.
	//
	// MUST run BEFORE publishOrderFilled: the leg still exists in h.pos here.
	// After publishOrderFilled emits KindPositionClosed, the leg is gone.
	var dustPnL decimal.Decimal
	if isClosingFill && !h.cfg.isFutures && legQty.IsPositive() {
		dust := legQty.Sub(cumulativeQty)
		filters := h.helm.FiltersFor(ctx, symbol)
		if filters.QtyStep.IsPositive() && dust.IsPositive() && dust.LessThan(filters.QtyStep) {
			dustPrice := price
			if mp := h.helm.LastKnownPrice(symbol); mp.IsPositive() {
				dustPrice = mp
			}
			h.log.Info("hand: dust reconciliation — sub-step residual returned to helm at market price",
				"symbol", symbol,
				"dust", dust, "price", dustPrice, "step", filters.QtyStep,
			)
			// Credit portfolio with the USDT value of the dust.
			h.helm.ReportFill(helmdomain.FillReport{
				HandID:    h.id.String(),
				HelmID:    h.helmID.String(),
				OrderID:   orderID + "_dust",
				Symbol:    symbol,
				Side:      side,
				Qty:       dust,
				Price:     dustPrice,
				Timestamp: time.Now().UTC(),
			})
			// Compute dust PnL so it can be folded into closePnL below.
			// No win/loss count — dust is part of the same trade.
			if entryPrice.IsPositive() {
				if side == "sell" {
					dustPnL = dustPrice.Sub(entryPrice).Mul(dust)
				} else {
					dustPnL = entryPrice.Sub(dustPrice).Mul(dust)
				}
				h.metrics.mu.Lock()
				h.metrics.totalPnL = h.metrics.totalPnL.Add(dustPnL)
				h.metrics.mu.Unlock()
			}
		}
	}
	closePnL = closePnL.Add(dustPnL)

	// ── 5. poslog ─────────────────────────────────────────────────────────────
	// publishOrderFilled updates h.pos (ActiveLegs) — must run BEFORE EmitEvent
	// so that observers (tests, SSE clients) see a consistent DeployedCapital.
	// deployedCapital = quote cost of THIS fill = qty×price + entry_fee_quote.
	// Zero for exit fills (no new capital is deployed on close).
	var deployedCapital decimal.Decimal
	if !isClosingFill {
		deployedCapital = cumulativeQty.Mul(cumulativeAvgPrice).Add(cumulativeCommission)
		h.metrics.mu.Lock()
		h.metrics.totalCommission = h.metrics.totalCommission.Add(cumulativeCommission)
		h.metrics.mu.Unlock()
	}
	// Pass legQty so publishOrderFilled uses the full position size in the trade
	// record (gross_pnl = legQty×exitPrice − legQty×entryPrice, which includes
	// the dust portion at approximately the same price).
	h.publishOrderFilled(ctx, orderID, cumulativeQty, legQty, cumulativeAvgPrice, closePnL, cumulativeCommission, deployedCapital, source, closeSource)

	// Delete exitLevels[tradeID] AFTER publishOrderFilled so that publishOrderFilled
	// can detect isBracketExit by finding the bracket order ID in exitLevels.
	// cancelExitOrders already ran above (marks sibling in pendingCancels and
	// launches a REST cancel goroutine) — it only needs exitLevels to exist during
	// its own execution, not during publishOrderFilled.
	if isClosingFill {
		h.mu.Lock()
		delete(h.exitLevels, tradeID)
		h.mu.Unlock()
	}

	// ── 5.5. Persist resolved IsOffset SL/TP into the leg ───────────────────
	// For offset entries the OrderPlaced poslog carried zero SL/TP (fill price
	// unknown at placement). Now that we've resolved absolute levels, emit
	// KindSLUpdated so:
	//   1. LegState.{StopLoss,TakeProfit} reflect the absolute prices — picked up by
	//      LegSnapshot → PositionClosedPayload → trade record's planned_risk + r_multiple.
	//   2. Poslog replay on restart restores the resolved levels independently of
	//      whether the bracket-order goroutine succeeded.
	// Must run AFTER publishOrderFilled so the leg is in PhaseOpen (applySLUpdated requires it).
	if offsetResolved && !isClosingFill && tradeID != "" &&
		(resolvedEl.StopLoss.IsPositive() || resolvedEl.TakeProfit.IsPositive()) {
		var newSL, newTP string
		if resolvedEl.StopLoss.IsPositive() {
			newSL = resolvedEl.StopLoss.String()
		}
		if resolvedEl.TakeProfit.IsPositive() {
			newTP = resolvedEl.TakeProfit.String()
		}
		payload, _ := json.Marshal(poslog.SLUpdatedPayload{
			OrderID: tradeID,
			NewSL:   newSL,
			NewTP:   newTP,
			Reason:  "offset_resolved",
		})
		h.publishAndApply(ctx, poslog.Event{
			ID:      tradeID + "_sl_resolved_" + orderID,
			HandID:  h.id.String(),
			HelmID:  h.helmID.String(),
			TradeID: tradeID,
			Kind:    poslog.KindSLUpdated,
			Payload: payload,
			At:      time.Now().UTC(),
		})
	}

	// ── 6. Non-WS fill publish ────────────────────────────────────────────────
	// source="ws"  → registry_fills.go already published with real FillID + fee.
	// other sources (poll, kill, …) → no WS echo expected; publish now.
	//                 MarkOrderFillPublished blocks Sync() re-publish.
	if source != "ws" {
		h.helm.RemoveOrderTracking(orderID)
		published := h.helm.PublishTradeFill(natsapi.TransactionMsg{
			HelmID:    h.helmID.String(),
			AccountID: h.helm.GetAccountID().String(),
			UserID:    h.helm.GetUserID().String(),
			HandID:    h.id.String(),
			TradeID:   "", // no exchange fill ID on poll/timeout paths; dedup falls back to helmID+orderID
			OrderID:   orderID,
			Kind:      "fill",
			Symbol:    symbol,
			Side:      side,
			Qty:       qty,
			AvgPrice:  price,
			Fee:       commission,
			FilledAt:  time.Now().UTC(),
		})
		if published {
			h.helm.MarkOrderFillPublished(orderID)
		}
	}

	equity := h.RealizedEquity()
	deployed := h.EntryCap()
	// Compute unrealized PnL across all open legs using last known price.
	var unrealized decimal.Decimal
	h.mu.RLock()
	for _, leg := range h.pos.ActiveLegs() {
		curPrice := h.helm.LastKnownPrice(leg.Symbol)
		if curPrice.IsPositive() && leg.EntryPrice.IsPositive() {
			if leg.Side == "buy" {
				unrealized = unrealized.Add(curPrice.Sub(leg.EntryPrice).Mul(leg.Qty.Abs()))
			} else {
				unrealized = unrealized.Add(leg.EntryPrice.Sub(curPrice).Mul(leg.Qty.Abs()))
			}
		}
	}
	h.mu.RUnlock()
	availCash := equity.Sub(deployed) // USDT liquid — not locked in open positions
	h.log.Info("order: filled",
		"symbol", symbol,
		"side", side,
		"qty", qty,
		"price", price,
		"order_id", orderID,
		"source", source,
		"pnl", closePnL,
		"available_cash", availCash,
		"deployed", deployed,
		"unrealized_pnl", unrealized,
		"equity", equity,
	)
	h.EmitEvent(natsapi.HelmEvent{
		Code:            eventcode.CodeOrderFilled,
		Symbol:          symbol,
		Side:            side,
		Qty:             qty,
		Price:           price,
		OrderID:         orderID,
		Reason:          source,
		Msg:             "order: filled",
		PnL:             closePnL,
		AvailableCash:   availCash,
		DeployedCapital: deployed,
		Equity:          equity,
	})

	// ── 6.5. Position lifecycle events ───────────────────────────────────────
	// Emitted after eventcode.CodeOrderFilled so the order event (low-level fact) always
	// precedes the position event (higher-level view) in the activity feed.
	// preFillPhase was captured before publishOrderFilled mutated leg state —
	// it reflects the leg's intent (entering vs adding vs exiting) at fill time.
	switch {
	case isClosingFill:
		// Position closed: carry realized PnL + pct so the UI can display the trade outcome.
		// pnl_pct is a fraction (FE ×100 to show %). entryPrice×legQty = notional deployed.
		var pnlPct decimal.Decimal
		if entryPrice.IsPositive() && legQty.IsPositive() {
			pnlPct = closePnL.Div(entryPrice.Mul(legQty))
		}
		h.EmitEvent(natsapi.HelmEvent{
			Code:            eventcode.CodePositionClosed,
			Symbol:          symbol,
			Side:            side,
			Qty:             cumulativeQty,
			Price:           cumulativeAvgPrice,
			PositionID:      tradeID,
			EntryPrice:      entryPrice,
			PnL:             closePnL,
			PnLPct:          pnlPct,
			Reason:          closeSource,
			Msg:             "position: closed",
			AvailableCash:   availCash,
			DeployedCapital: deployed,
			Equity:          equity,
		})
	case preFillPhase == position.PhaseAdding:
		// Pyramid add confirmed — show total position qty and new blended avg entry.
		var newAvgEntry, totalQty decimal.Decimal
		h.mu.RLock()
		if snap, ok := h.pos.LegSnapshot(tradeID); ok {
			newAvgEntry = snap.EntryPrice
			totalQty = snap.Qty.Abs()
		}
		h.mu.RUnlock()
		h.EmitEvent(natsapi.HelmEvent{
			Code:            eventcode.CodePositionAdded,
			Symbol:          symbol,
			Side:            side,
			Qty:             totalQty,           // total position qty after add
			Price:           cumulativeAvgPrice, // this add's fill price
			PositionID:      tradeID,
			EntryPrice:      newAvgEntry, // new blended avg across all legs
			Reason:          source,
			Msg:             "position: pyramid add",
			AvailableCash:   availCash,
			DeployedCapital: deployed,
			Equity:          equity,
		})
	case preFillPhase == position.PhaseEntering:
		// New position confirmed open.
		h.EmitEvent(natsapi.HelmEvent{
			Code:            eventcode.CodePositionOpened,
			Symbol:          symbol,
			Side:            side,
			Qty:             cumulativeQty,
			Price:           cumulativeAvgPrice,
			PositionID:      tradeID,
			EntryPrice:      cumulativeAvgPrice,
			Reason:          source,
			Msg:             "position: opened",
			AvailableCash:   availCash,
			DeployedCapital: deployed,
			Equity:          equity,
		})
	}

}

// applyEntryFill handles the entry path for fills where the leg phase is
// PhaseEntering or PhaseAdding: resolves pending exit levels, caches WS-before-REST
// fill data, and schedules exchange-side bracket order placement.
func (h *Hand) applyEntryFill(ctx context.Context, symbol, orderID, tradeID string,
	cumulativeQty, cumulativeAvgPrice, cumulativeCommission decimal.Decimal,
) (resolvedEl exitLevel, offsetResolved bool) {
	bracketQty := cumulativeQty
	var shouldPlaceBracket bool
	var oldBracketIDs []string
	var tradeIDForBracket string

	h.mu.Lock()
	tradeIDForBracket = h.pendingOrderPos[orderID]
	// WS-before-REST: pendingOrderPos not yet set because PlaceOrder has not returned.
	// Cache fill data so applyPlaceResult can complete poslog + bracket after the REST ack.
	if tradeIDForBracket == "" {
		h.wsFillCache[orderID] = cachedWsFill{
			qty:        cumulativeQty,
			price:      cumulativeAvgPrice,
			commission: cumulativeCommission,
		}
	}
	if pending, ok := h.pendingExits[orderID]; ok {
		delete(h.pendingExits, orderID)
		// Pre-add leg state (LegSnapshot is pre-fill — this fill is applied later via
		// publishOrderFilled). Drives both the blended-avg anchor and the merged bracket qty.
		var preQty, preAvg decimal.Decimal
		if snap, ok := h.pos.LegSnapshot(tradeIDForBracket); ok && snap.Qty.IsPositive() {
			preQty, preAvg = snap.Qty, snap.EntryPrice
			bracketQty = preQty.Add(cumulativeQty) // merged total
		}
		// Capture existing bracket IDs BEFORE resolvePendingExit cancels them.
		// Used after the lock is released to emit a bracket-replaced event.
		if preQty.IsPositive() {
			if old, ok := h.exitLevels[tradeIDForBracket]; ok {
				oldBracketIDs = append([]string(nil), old.ExchangeOrderIDs...)
			}
		}
		resolvedEl, shouldPlaceBracket, offsetResolved = h.resolvePendingExit(ctx, symbol, tradeIDForBracket, pending, cumulativeAvgPrice, cumulativeQty, preQty, preAvg)
	}
	h.mu.Unlock()

	// When a pyramid add fills, resolvePendingExit cancels the old OCO bracket and
	// schedules a new one covering the merged position. Emit an explicit event so
	// users understand why an OCO bracket was cancelled — it is not an external close.
	if len(oldBracketIDs) > 0 && shouldPlaceBracket {
		h.EmitEvent(natsapi.HelmEvent{
			Code:    eventcode.CodeOrderCancelled,
			Symbol:  symbol,
			OrderID: fmt.Sprintf("%v", oldBracketIDs),
			Reason:  "pyramid_add_bracket_replace",
			Msg:     "order: OCO bracket cancelled — pyramid add will place new merged bracket",
		})
	}

	if shouldPlaceBracket {
		h.scheduleBracketPlacement(resolvedEl, symbol, bracketQty, tradeIDForBracket)
	}

	return resolvedEl, offsetResolved
}

// applyExitFill handles the exit path for fills where the leg phase is PhaseExiting
// or the fill matches a bracket OCO order (isBracketExit). It cancels the sibling
// bracket order, classifies the close source (sl/tp/signal/kill/…), computes realized
// PnL, and updates hand metrics.
func (h *Hand) applyExitFill(ctx context.Context, symbol, orderID, side, source, tradeID string,
	isBracketExit bool, cumulativeAvgPrice, cumulativeQty, cumulativeCommission decimal.Decimal,
) (legQty, entryPrice, closePnL decimal.Decimal, closeSource string) {

	h.mu.Lock()
	// Read exitLevels BEFORE cancelling — needed to classify bracket exit as sl/tp.
	// NOTE: do NOT delete exitLevels[tradeID] here. publishOrderFilled (called in the
	// shared tail of applyFill) needs exitLevels[tradeID] intact to detect isBracketExit.
	// The delete is deferred to after publishOrderFilled in applyFill.
	el := h.exitLevels[tradeID]
	h.cancelExitOrders(ctx, tradeID, symbol, orderID)
	h.mu.Unlock()

	var deployedCapital decimal.Decimal
	h.mu.RLock()
	entryPrice = h.pos.LegEntryPrice(tradeID)
	if snap, ok := h.pos.LegSnapshot(tradeID); ok {
		legQty = snap.Qty.Abs()
		deployedCapital = snap.DeployedCapital
	}
	h.mu.RUnlock()

	switch {
	case isBracketExit:
		// Determine whether the SL or TP leg of the OCO filled by comparing
		// the fill price to the saved levels. Side == "buy" means entry was
		// a long (exit sell): SL is below entry, TP is above entry.
		//
		// Primary: strict directional check (fill clearly past the level).
		// Fallback: proximity — when the exchange uses market-price execution
		// (e.g. OKX tpOrdPx=-1), the actual fill can be 1-2 ticks inside the
		// trigger. Strict check fails; pick whichever level the fill is closest to.
		closeSource = "bracket_exit" // fallback if levels unavailable
		if el.StopLoss.IsPositive() && el.TakeProfit.IsPositive() {
			if el.Side == "buy" { // long: SL below entry, TP above entry
				if cumulativeAvgPrice.LessThanOrEqual(el.StopLoss) {
					closeSource = string(poslog.ExitReasonStopLoss)
				} else if cumulativeAvgPrice.GreaterThanOrEqual(el.TakeProfit) {
					closeSource = string(poslog.ExitReasonTakeProfit)
				} else {
					distSL := cumulativeAvgPrice.Sub(el.StopLoss).Abs()
					distTP := cumulativeAvgPrice.Sub(el.TakeProfit).Abs()
					if distTP.LessThanOrEqual(distSL) {
						closeSource = string(poslog.ExitReasonTakeProfit)
					} else {
						closeSource = string(poslog.ExitReasonStopLoss)
					}
				}
			} else { // short: TP below entry, SL above entry
				if cumulativeAvgPrice.GreaterThanOrEqual(el.StopLoss) {
					closeSource = string(poslog.ExitReasonStopLoss)
				} else if cumulativeAvgPrice.LessThanOrEqual(el.TakeProfit) {
					closeSource = string(poslog.ExitReasonTakeProfit)
				} else {
					distSL := cumulativeAvgPrice.Sub(el.StopLoss).Abs()
					distTP := cumulativeAvgPrice.Sub(el.TakeProfit).Abs()
					if distTP.LessThanOrEqual(distSL) {
						closeSource = string(poslog.ExitReasonTakeProfit)
					} else {
						closeSource = string(poslog.ExitReasonStopLoss)
					}
				}
			}
		}
		switch poslog.ExitReason(closeSource) {
		case poslog.ExitReasonTakeProfit:
			h.log.Info("hand: TP triggered — take-profit bracket filled",
				"symbol", symbol, "order_id", orderID,
				"fill_price", cumulativeAvgPrice, "tp_level", el.TakeProfit)
		case poslog.ExitReasonStopLoss:
			h.log.Info("hand: SL triggered — stop-loss bracket filled",
				"symbol", symbol, "order_id", orderID,
				"fill_price", cumulativeAvgPrice, "sl_level", el.StopLoss)
		default:
			h.log.Info("hand: bracket exit filled (TP/SL level unknown)",
				"symbol", symbol, "order_id", orderID,
				"fill_price", cumulativeAvgPrice,
				"sl_level", el.StopLoss, "tp_level", el.TakeProfit)
		}
	case source == "kill":
		closeSource = "kill"
	case source == "release":
		closeSource = "release"
	case source == "dust_exit":
		closeSource = "external"
	default:
		// rest / ws / poll / partial_cancel — all mean a strategy DirExit
		// signal or local exit monitor triggered the closing order.
		closeSource = string(poslog.ExitReasonSignal)
	}

	if cumulativeAvgPrice.IsPositive() && entryPrice.IsPositive() {
		if side == "sell" { // closing a long position
			closePnL = cumulativeAvgPrice.Sub(entryPrice).Mul(cumulativeQty)
		} else { // closing a short position (buy to cover)
			closePnL = entryPrice.Sub(cumulativeAvgPrice).Mul(cumulativeQty)
		}
		// netPnL nets out BOTH sides' commission (entry + this exit) so win/loss
		// classification matches the Net P&L shown to the user — a trade whose price
		// moved favorably but whose fees exceed that gain is a loss, not a win.
		// Entry commission is backed out of DeployedCapital (= sum(qty*price + fee)
		// across all entry fills for this leg) and prorated by the fraction of the
		// leg this fill closes, so partial exits don't over-subtract.
		netPnL := closePnL.Sub(cumulativeCommission)
		if legQty.IsPositive() && deployedCapital.IsPositive() {
			entryCommission := deployedCapital.Sub(legQty.Mul(entryPrice))
			if entryCommission.IsPositive() {
				netPnL = netPnL.Sub(entryCommission.Mul(cumulativeQty).Div(legQty))
			}
		}

		h.metrics.mu.Lock()
		h.metrics.totalPnL = h.metrics.totalPnL.Add(closePnL)
		h.metrics.totalCommission = h.metrics.totalCommission.Add(cumulativeCommission)
		// dust_exit is a synthetic rounding fill — same trade, not a new trade event.
		// Accumulate PnL for accurate totals, but skip win/loss count and edge risk.
		if source != "dust_exit" {
			if netPnL.IsPositive() {
				h.metrics.winCount++
			} else {
				h.metrics.lossCount++
			}
		}
		h.metrics.mu.Unlock()
		if source != "dust_exit" {
			h.checkEdgeRisk(netPnL)
		}
	}

	return legQty, entryPrice, closePnL, closeSource
}

// checkEdgeRisk evaluates the per-hand sliding-window edge-degradation guard.
// Called after every closing fill from the run-loop goroutine (no extra lock needed
// for the ring buffer fields — they are exclusively written here).
// Auto-pauses the hand when any enabled threshold is breached.
func (h *Hand) checkEdgeRisk(pnl decimal.Decimal) {
	cfg := h.guard.cfg
	if cfg.WindowTrades == 0 {
		return
	}

	// ── 1. Push PnL into ring ─────────────────────────────────────────────────
	h.guard.push(pnl)

	// ── 2. Update consecutive-loss streak ────────────────────────────────────
	if pnl.IsNegative() {
		h.guard.consecLoss++
	} else {
		h.guard.consecLoss = 0
	}

	// ── 3. Determine active window size ──────────────────────────────────────
	count := cfg.WindowTrades
	if !h.guard.full {
		count = h.guard.head // number of pushes so far; head hasn't wrapped yet
		if count == 0 {
			return
		}
	}

	// ── 4. Compute window stats ───────────────────────────────────────────────
	sum := decimal.Zero
	minPnL := h.guard.ring[0]
	for i := 0; i < count; i++ {
		sum = sum.Add(h.guard.ring[i])
		if h.guard.ring[i].LessThan(minPnL) {
			minPnL = h.guard.ring[i]
		}
	}
	avg := sum.Div(decimal.NewFromInt(int64(count)))

	// Reference capital for pct thresholds: realized equity (allocatedCap + closedPnL −
	// commission) so the guard tightens naturally as the hand loses money, rather than
	// referencing a fixed allocation that becomes stale after repeated losses.
	// Falls back to total portfolio equity when RealizedEquity is zero (no allocation set).
	ref := h.RealizedEquity()
	if !ref.IsPositive() {
		ref = h.helm.GetPortfolio().Equity()
	}
	if !ref.IsPositive() {
		return
	}

	// ── 5. Check thresholds ───────────────────────────────────────────────────
	pct := func(v decimal.Decimal) float64 {
		return v.Div(ref).Mul(decimal.NewFromInt(100)).InexactFloat64()
	}

	var breachReason string
	switch {
	case cfg.MaxTotalLossPct > 0 && sum.LessThan(ref.Mul(decimal.NewFromFloat(-cfg.MaxTotalLossPct))):
		breachReason = fmt.Sprintf("window total loss %.2f%% > max %.2f%% (last %d trades)",
			-pct(sum), cfg.MaxTotalLossPct*100, count)

	case cfg.MaxAvgLossPct > 0 && avg.LessThan(ref.Mul(decimal.NewFromFloat(-cfg.MaxAvgLossPct))):
		breachReason = fmt.Sprintf("window avg loss %.2f%% > max %.2f%% (last %d trades)",
			-pct(avg), cfg.MaxAvgLossPct*100, count)

	case cfg.MaxSingleLossPct > 0 && minPnL.LessThan(ref.Mul(decimal.NewFromFloat(-cfg.MaxSingleLossPct))):
		breachReason = fmt.Sprintf("single trade loss %.2f%% > max %.2f%%",
			-pct(minPnL), cfg.MaxSingleLossPct*100)

	case cfg.MaxConsecLoss > 0 && h.guard.consecLoss >= cfg.MaxConsecLoss:
		breachReason = fmt.Sprintf("consecutive losses %d reached max %d", h.guard.consecLoss, cfg.MaxConsecLoss)
	}

	if breachReason == "" {
		return
	}

	// ── 6. Auto-stop ──────────────────────────────────────────────────────────
	h.log.Warn("hand: edge degradation detected — auto-stopping",
		"reason", breachReason)
	h.EmitEvent(natsapi.HelmEvent{
		Code:   eventcode.CodeHandAutoStopped,
		Reason: "edge risk: " + breachReason,
		Msg:    "hand: auto-stopped — edge degradation",
	})
	go h.Stop()
}

// scheduleBracketPlacement launches the exchange OCO bracket placement goroutine.
// Shared between the entry-fill path and the post-restart recovery path.
// bracketQty is the full position size to protect. tradeID is the poslog position ID
// (used to write KindBracketPlaced on success; empty string skips poslog write).
func (h *Hand) scheduleBracketPlacement(lv exitLevel, symbol string, bracketQty decimal.Decimal, tradeID string) {
	placer, ok := h.helm.GetExchange().(exchange.ExitOrderPlacer)
	if !ok {
		return
	}
	exitSide := exchange.Sell
	if lv.Side == "sell" { // short → buy to close
		exitSide = exchange.Buy
	}
	market := exchange.MarketSpot
	if h.cfg.isFutures {
		market = exchange.MarketFutures
	}
	h.mu.RLock()
	handCtx := h.ctx
	h.mu.RUnlock()

	go func(el exitLevel) {
		defer safe.Recover()
		bktCtx := handCtx
		var marginMode string
		if h.cfg.futuresConfig != nil {
			marginMode = string(h.cfg.futuresConfig.MarginType)
		}
		// Use actual leg qty at execution time (after publishOrderFilled has set it),
		// not the scheduled bracketQty which may be the ordered qty when partial fills
		// resulted in a smaller actual position (e.g. OKX Unified Account buy capped
		// by available margin, or quote_qty order filled below expected base qty).
		// Fall back to bracketQty if the leg is already gone (position closed by a
		// concurrent exit before the bracket goroutine ran).
		actualBracketQty := bracketQty
		h.mu.RLock()
		for _, l := range h.pos.ActiveLegs() {
			if l.TradeID == tradeID && l.Qty.IsPositive() {
				actualBracketQty = l.Qty
				break
			}
		}
		h.mu.RUnlock()
		bracketQtyTruncated := TruncateQty(h.helm.FiltersFor(bktCtx, symbol), actualBracketQty)
		// Generated once, before the retry loop — every attempt reuses the same
		// clid (idempotent: a lost-response retry either lands as a genuine
		// duplicate-clid rejection at the exchange, or resolves later via
		// reconcile's ambiguous-bracket recovery, same as entry/exit orders).
		clientOrderID := clid.New()
		if tradeID != "" {
			h.publishBracketPlace(bktCtx, tradeID, symbol, clientOrderID, bracketQtyTruncated, el.StopLoss, el.TakeProfit)
		}
		exitReq := exchange.ExitOrderRequest{
			Symbol:        symbol,
			Market:        market,
			Side:          exitSide,
			Qty:           bracketQtyTruncated,
			StopLoss:      el.StopLoss,
			TakeProfit:    el.TakeProfit,
			MarginMode:    marginMode,
			ClientOrderID: clientOrderID,
		}
		var result *exchange.ExitOrderResult
		var err error
		for attempt := 1; attempt <= 5; attempt++ {
			select {
			case <-handCtx.Done():
				h.log.Info("hand: exit order goroutine cancelled (hand stopped)", "symbol", symbol)
				return
			case <-time.After(time.Duration(attempt) * time.Second):
			}
			// Detached from handCtx deliberately: this is a single in-flight HTTP call to
			// place a real exchange order, and aborting it mid-flight on Stop() leaves the
			// exchange-side state ambiguous (did the bracket land or not?) — same failure
			// shape as cancelExitOrders's h.ctx race (hand_exits.go). The retry loop's OWN
			// decision to attempt another try still checks handCtx.Done() below, so a
			// stopped hand still stops retrying between attempts — only an attempt already
			// in flight is allowed to finish instead of being cut short.
			exitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			result, err = placer.PlaceExitOrders(exitCtx, h.helm.GetCreds(), exitReq)
			cancel()
			if err == nil {
				break
			}
			h.log.Warn("hand: place exit orders retry", "symbol", symbol,
				"attempt", attempt, "err", err)
		}
		if err != nil {
			h.log.Error("hand: place exit orders failed — position now relies on the local monitor only",
				"symbol", symbol, "err", err)
			h.helm.EmitEvent(natsapi.HelmEvent{
				HandID: h.id.String(),
				Code:   eventcode.CodeOrderExitFailed,
				Symbol: symbol,
				Reason: fmt.Sprintf("qty=%s stop_loss=%s take_profit=%s err=%s", exitReq.Qty, el.StopLoss, el.TakeProfit, err),
				Msg:    "order: exchange SL/TP bracket FAILED — local monitor is the only net",
			})
			return
		}
		h.log.Info("hand: exit orders placed", "symbol", symbol, "order_ids", result.OrderIDs)
		h.helm.EmitEvent(natsapi.HelmEvent{
			HandID:  h.id.String(),
			Code:    eventcode.CodeOrderExitPlaced,
			Symbol:  symbol,
			OrderID: fmt.Sprintf("%v", result.OrderIDs),
			Reason:  fmt.Sprintf("stop_loss=%s take_profit=%s", el.StopLoss, el.TakeProfit),
			Msg:     "order: bracket exit placed (safety net)",
		})
		for _, id := range result.OrderIDs {
			h.trackOrder(id)
		}
		h.mu.Lock()
		if cur, ok := h.exitLevels[tradeID]; ok {
			cur.ExchangeOrderIDs = result.OrderIDs
			cur.GroupID = result.GroupID
			cur.PlacedAt = time.Now()
			h.exitLevels[tradeID] = cur
		}
		h.mu.Unlock()
		if tradeID != "" {
			// Use the clid we generated (clientOrderID), not result.ClientOrderID —
			// we already know it regardless of whether this adapter echoes it back
			// in the response, and every adapter isn't wired to send/echo it yet.
			h.publishBracketPlaced(handCtx, tradeID, symbol, clientOrderID, result.GroupID, result.OrderIDs)
		}
	}(lv)
}

// RestoreExitLevelsFromPoslog rebuilds exitLevels from the durable poslog-backed
// position state (h.pos) after a restart.
//
// exitLevels is an ephemeral in-memory map — it is populated at runtime by
// resolvePendingExit / scheduleBracketPlacement and is NOT persisted. After a
// restart it starts empty even though the poslog has all the information:
//
//	KindSLUpdated     → leg.StopLoss / TakeProfit
//	KindBracketPlaced → leg.ExchangeOrderIDs
//
// Without this reconstruction:
//   - RecoverBrackets iterates exitLevels and finds nothing → bracket recovery skipped
//   - checkPositionDesync sees hasExit=false → leg treated as UNPROTECTED → EXT_CLOSE
func (h *Hand) RestoreExitLevelsFromPoslog() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, leg := range h.pos.ActiveLegs() {
		if leg.Phase != position.PhaseOpen {
			h.log.Debug("startup: restoreExitLevels — leg not PhaseOpen, skip",
				"symbol", leg.Symbol, "phase", leg.Phase)
			continue
		}
		if !leg.HasExitManagement() {
			h.log.Info("startup: restoreExitLevels — leg has no exit management (no SL/TP, no OCO ids)",
				"symbol", leg.Symbol,
				"qty", leg.Qty, "entry_price", leg.EntryPrice)
			continue
		}
		lv := exitLevel{
			Symbol:           leg.Symbol,
			Side:             leg.Side,
			StopLoss:         leg.StopLoss,
			TakeProfit:       leg.TakeProfit,
			ExchangeOrderIDs: append([]string(nil), leg.ExchangeOrderIDs...),
			GroupID:          leg.GroupID,
		}
		h.exitLevels[leg.TradeID] = lv
		h.log.Info("startup: restoreExitLevels — exit level restored from poslog",
			"symbol", leg.Symbol,
			"qty", leg.Qty,
			"sl", lv.StopLoss,
			"tp", lv.TakeProfit,
			"exchange_order_ids", lv.ExchangeOrderIDs,
			"has_oco", len(lv.ExchangeOrderIDs) > 0,
		)
	}
}

// RecoverBrackets reconciles exchange-side OCO bracket orders after a restart.
//
// Three scenarios handled:
//  1. ExchangeOrderIDs empty (crash before KindBracketPlaced) → re-place bracket.
//  2. ExchangeOrderIDs present, bracket already triggered during downtime ("filled") →
//     synthesize a bracket fill now so the position closes cleanly before Sync() runs.
//     Without this, Sync() detects a portfolio desync and emits EXT_CLOSE with an
//     approximate price instead of recording the real OCO execution price.
//  3. ExchangeOrderIDs present, bracket cancelled externally during downtime →
//     route to HandleExitOrderCanceled (disown + orphan detection).
//
// Safe to call on a running hand — uses the actor's mutex and launches goroutines.
// Must be called after RecoverGapFills so fills from downtime are already applied
// (prevents re-placing a bracket for a position that is now flat).
func (h *Hand) RecoverBrackets(ctx context.Context) {
	h.mu.RLock()
	type toPlaceEntry struct {
		symbol  string
		lv      exitLevel
		tradeID string
		qty     decimal.Decimal
	}
	type toCheckEntry struct {
		symbol string
		id     string
	}
	var toPlace []toPlaceEntry
	var toCheck []toCheckEntry

	h.log.Info("startup: RecoverBrackets — scanning exitLevels",
		"exit_levels_count", len(h.exitLevels))

	for tradeID, lv := range h.exitLevels {
		symbol := lv.Symbol
		if len(lv.ExchangeOrderIDs) > 0 {
			h.log.Info("startup: RecoverBrackets — checking known bracket IDs",
				"symbol", symbol,
				"sl", lv.StopLoss, "tp", lv.TakeProfit,
				"order_ids", lv.ExchangeOrderIDs)
			for _, id := range lv.ExchangeOrderIDs {
				toCheck = append(toCheck, toCheckEntry{symbol: symbol, id: id})
			}
			continue
		}
		if !lv.StopLoss.IsPositive() && !lv.TakeProfit.IsPositive() {
			continue
		}
		h.log.Info("startup: RecoverBrackets — no OCO ids, will re-place bracket",
			"symbol", symbol,
			"sl", lv.StopLoss, "tp", lv.TakeProfit)
		// Match the exact leg this exitLevels entry belongs to (by TradeID), not just
		// "the" leg for this symbol — a symbol can have multiple independent legs
		// (non-pyramid, MaxUnits>1), each needing its own bracket re-placed.
		for _, l := range h.pos.ActiveLegs() {
			if l.TradeID == tradeID {
				toPlace = append(toPlace, toPlaceEntry{
					symbol: symbol, lv: lv,
					tradeID: tradeID, qty: l.Qty.Abs(),
				})
				break
			}
		}
	}
	h.mu.RUnlock()

	// ── Scenario 2 & 3: check brackets with known IDs ────────────────────────
	for _, c := range toCheck {
		result, err := h.helm.GetExchange().GetOrder(ctx, h.helm.GetCreds(), c.id)
		if err != nil {
			h.log.Warn("startup: RecoverBrackets — GetOrder failed",
				"symbol", c.symbol, "order_id", c.id, "err", err)
			continue
		}
		h.log.Info("startup: RecoverBrackets — bracket status from exchange",
			"symbol", c.symbol, "order_id", c.id,
			"status", result.Status,
			"filled_qty", result.FilledQty,
			"filled_avg", result.FilledAvg,
		)
		switch result.Status {
		case "filled":
			// Bracket was triggered and filled during downtime.
			// Route through EnqueueFill (actor loop) rather than calling applyFill
			// directly — RecoverBrackets runs while the hand's actor loop is already
			// active (called after StartAllHydrated), so direct applyFill is a race.
			h.mu.Lock()
			if _, alreadySeen := h.seenFills[c.id]; alreadySeen {
				h.mu.Unlock()
				continue
			}
			h.seenFills[c.id] = time.Now()
			h.mu.Unlock()

			closeSide := string(result.Side)
			if closeSide == "" {
				closeSide = "sell"
			}
			h.log.Info("hand: bracket filled during downtime — enqueuing recovery fill",
				"symbol", c.symbol, "order_id", c.id,
				"filled_qty", result.FilledQty, "filled_avg", result.FilledAvg)
			if result.FilledQty.IsPositive() && result.FilledAvg.IsPositive() {
				h.EnqueueFill(exchange.WsFillEvent{
					OrderID:   c.id,
					FillID:    c.id + "_recovery",
					Symbol:    c.symbol,
					Side:      exchange.OrderSide(closeSide),
					FilledQty: result.FilledQty,
					FilledAvg: result.FilledAvg,
				})
			}

		case "canceled", "cancelled", "expired", "rejected":
			// Bracket was externally cancelled during downtime.
			h.log.Info("hand: bracket cancelled during downtime — handling external close",
				"symbol", c.symbol, "order_id", c.id, "status", result.Status)
			h.HandleExitOrderCanceled(ctx, c.id)

		default:
			// "new" / "submitted" — bracket still live; no action needed.
			h.log.Debug("hand: bracket still live after restart",
				"symbol", c.symbol, "order_id", c.id, "status", result.Status)
		}
	}

	// ── Scenario 1: re-place brackets where IDs were never recorded ──────────
	// Before placing a new bracket, query the exchange for existing live algo orders
	// for each symbol. If a matching OCO already exists (placed before the crash but
	// before KindBracketPlaced was written), adopt its IDs instead of creating a
	// duplicate — two concurrent OCOs for the same position would cause "insufficient
	// balance" errors when the second tries to sell a position already closed by the first.
	algoLister, hasAlgoLister := h.helm.GetExchange().(exchange.AlgoOrderLister)

	for _, p := range toPlace {
		if p.qty.IsZero() {
			continue
		}

		// Check for existing live algo orders matching this symbol/SL/TP.
		if hasAlgoLister {
			existing, err := algoLister.ListLiveAlgoOrders(ctx, h.helm.GetCreds(), p.symbol)
			if err != nil {
				h.log.Warn("hand: RecoverBrackets — ListLiveAlgoOrders failed (will re-place)",
					"symbol", p.symbol, "err", err)
			} else {
				var adopted string
				for _, ao := range existing {
					// Match by SL/TP proximity (within 1 tick) — exact match unreliable
					// due to rounding in the original PlaceExitOrders call.
					slMatch := p.lv.StopLoss.IsZero() || p.lv.StopLoss.Sub(ao.StopLoss).Abs().LessThanOrEqual(decimal.NewFromFloat(0.01))
					tpMatch := p.lv.TakeProfit.IsZero() || p.lv.TakeProfit.Sub(ao.TakeProfit).Abs().LessThanOrEqual(decimal.NewFromFloat(0.01))
					if slMatch && tpMatch {
						adopted = ao.AlgoID
						break
					}
				}
				if adopted != "" {
					h.log.Info("hand: found existing live algo — adopting IDs instead of re-placing",
						"symbol", p.symbol, "algo_id", adopted)
					// ListLiveAlgoOrders returns the raw algoId (no "instID:A:" prefix) —
					// format it the same way PlaceExitOrders does so a later CancelOrder
					// correctly routes it through parseOKXOrderID's algo branch instead of
					// misreading it as a regular order id.
					adoptedOrderID := p.symbol + ":A:" + adopted
					h.trackOrder(adoptedOrderID)
					h.mu.Lock()
					if lv, ok := h.exitLevels[p.tradeID]; ok {
						lv.ExchangeOrderIDs = []string{adoptedOrderID}
						lv.GroupID = adopted
						h.exitLevels[p.tradeID] = lv
					}
					h.mu.Unlock()
					h.publishBracketPlaced(ctx, p.tradeID, p.symbol, "", adopted, []string{adoptedOrderID})
					continue
				}
			}
		}

		h.log.Info("hand: re-placing bracket after restart — KindBracketPlaced was not confirmed",
			"symbol", p.symbol,
			"stop_loss", p.lv.StopLoss, "take_profit", p.lv.TakeProfit, "qty", p.qty)
		h.scheduleBracketPlacement(p.lv, p.symbol, p.qty, p.tradeID)
	}
}

// resolvePendingExit resolves raw SL/TP from a just-filled entry order into an absolute
// exitLevel, updates h.exitLevels[tradeID], and returns whether a bracket should be placed.
//
// MUST be called with h.mu held (for exitLevels read/write and cancelExitOrders).
//
// price/qty are the fill price and fill qty.
// preQty/preAvg are the leg's qty/avg BEFORE this fill (zero for first entry).
//
// Returns (resolved exitLevel, shouldPlaceBracket, offsetResolved).
func (h *Hand) resolvePendingExit(
	ctx context.Context,
	symbol, tradeID string, pending exitPending,
	price, qty, preQty, preAvg decimal.Decimal,
) (resolvedEl exitLevel, shouldPlaceBracket bool, offsetResolved bool) {
	resolved := exitLevel{Side: pending.Side, Symbol: symbol}
	if pending.IsOffset {
		// ── avg-anchor (pyramiding-design.md) ──
		// Anchor offsets to the blended avg entry AFTER this add so the SL/TP tracks
		// the merged cost. First entry: preQty=0 → anchor == fill price.
		anchor := price
		if preQty.IsPositive() {
			if newQty := preQty.Add(qty); newQty.IsPositive() {
				anchor = preQty.Mul(preAvg).Add(qty.Mul(price)).Div(newQty)
			}
		}
		if anchor.IsPositive() {
			prec := PriceTick(h.helm.FiltersFor(ctx, symbol).PriceTick)
			// Offsets are magnitudes (positive values) from the Rhai script convention.
			// Apply direction-aware sign: for a long, SL is below anchor and TP is above;
			// for a short, SL is above anchor and TP is below.
			// Using Abs() makes the code robust to scripts that already send signed values.
			isBuy := pending.Side == "buy"
			if !pending.StopOffset.IsZero() {
				dist := pending.StopOffset.Abs()
				if isBuy {
					resolved.StopLoss = anchor.Sub(dist).Round(prec) // long SL below entry
				} else {
					resolved.StopLoss = anchor.Add(dist).Round(prec) // short SL above entry
				}
			}
			if !pending.TakeProfitOffset.IsZero() {
				dist := pending.TakeProfitOffset.Abs()
				if isBuy {
					resolved.TakeProfit = anchor.Add(dist).Round(prec) // long TP above entry
				} else {
					resolved.TakeProfit = anchor.Sub(dist).Round(prec) // short TP below entry
				}
			}
		}
		offsetResolved = true
	} else {
		prec := PriceTick(h.helm.FiltersFor(ctx, symbol).PriceTick)
		if pending.StopLoss.IsPositive() {
			resolved.StopLoss = pending.StopLoss.Round(prec)
		}
		if pending.TakeProfit.IsPositive() {
			resolved.TakeProfit = pending.TakeProfit.Round(prec)
		}
		// Persist absolute SL/TP to poslog so they survive restart.
		// offsetResolved is true only for offset mode — for absolute levels we
		// must emit KindSLUpdated here so RestoreExitLevelsFromPoslog can rebuild
		// exitLevels with correct levels on restart (needed for bracket re-placement
		// in the crash-window scenario where KindBracketPlaced was not written).
		if resolved.StopLoss.IsPositive() || resolved.TakeProfit.IsPositive() {
			offsetResolved = true // reuse the flag to trigger KindSLUpdated in applyFill
		}
	}

	// Pyramid add: cancel prior consolidated bracket before overwriting exitLevels.
	if old, ok := h.exitLevels[tradeID]; ok && len(old.ExchangeOrderIDs) > 0 {
		h.cancelExitOrders(ctx, tradeID, symbol, "")
	}
	h.exitLevels[tradeID] = resolved

	if resolved.StopLoss.IsPositive() || resolved.TakeProfit.IsPositive() {
		slTpOK := true
		if resolved.StopLoss.IsPositive() && resolved.TakeProfit.IsPositive() {
			if resolved.Side == "buy" { // long → SL < TP
				slTpOK = resolved.StopLoss.LessThan(resolved.TakeProfit)
			} else { // short → TP < SL
				slTpOK = resolved.TakeProfit.LessThan(resolved.StopLoss)
			}
			if !slTpOK {
				h.log.Warn("fill: SL/TP relationship invalid after rounding — skipping OCO bracket",
					"symbol", symbol, "side", resolved.Side,
					"stop_loss", resolved.StopLoss, "take_profit", resolved.TakeProfit)
			}
		}
		if slTpOK {
			resolvedEl = resolved
			shouldPlaceBracket = true
		}
	}
	return
}

// completeWsFillFromREST is called in the REST-immediate path when the WS fill event
// arrived before PlaceOrder returned (WS beats REST race).
//
// The WS path ran applyFill which:
//
//	✅ Called ReportFill (portfolio correctly updated)
//	❌ publishOrderFilled returned early — pendingOrderPos[orderID] was "" at WS time
//	   → leg stuck in PhaseEntering, never transitions to PhaseOpen
//	   → h.pos.ActiveCount()>0 blocks new entries (MAXUNITS)
//	   → EXIT signal sees no PhaseOpen leg → "NO_POS"
//	❌ pendingExits not consumed (not set at WS time) → no bracket
//
// Now that PlaceOrder has returned, pendingOrderPos and pendingExits are both populated.
// This method completes the fill processing without re-running ReportFill:
//  1. publishOrderFilled → leg PhaseEntering → PhaseOpen (poslog + h.pos)
//  2. Resolve pendingExits → schedule bracket
//  3. Emit KindSLUpdated if offset levels were resolved (needed for poslog replay)
//
// pending is the exitPending captured from line 678 of applyPlaceResult (already in scope).
// restQty is the net fill qty after base-asset commission adjustment.
func (h *Hand) completeWsFillFromREST(
	ctx context.Context,
	orderID, symbol, side string,
	restQty, fillAvg, commission decimal.Decimal,
	pending exitPending,
	isExitOrder bool,
) {
	h.log.Info("hand: WS beat REST — completing poslog fill and bracket placement",
		"order_id", orderID, "symbol", symbol,
		"qty", restQty, "price", fillAvg)

	// Capture tradeID BEFORE publishOrderFilled deletes it from pendingOrderPos.
	h.mu.RLock()
	tradeIDForBracket := h.pendingOrderPos[orderID]
	h.mu.RUnlock()

	// 1. publishOrderFilled: transitions leg from PhaseEntering → PhaseOpen
	//    (entry) or PhaseExiting → PhaseIdle (exit).
	//    Does NOT call ReportFill (portfolio already updated by WS applyFill).
	deployedCapital := restQty.Mul(fillAvg).Add(commission)
	if isExitOrder {
		deployedCapital = decimal.Zero // no new capital deployed on close
	}
	h.publishOrderFilled(ctx, orderID, restQty, restQty, fillAvg,
		decimal.Zero, commission, deployedCapital, "rest", "")

	// 1b. For exit orders: cancel the OCO bracket and clear exitLevels.
	// The WS applyFill path could not detect isClosingFill (pendingOrderPos was
	// empty at WS arrival time), so cancelExitOrders was never called. Without
	// this, bracket orders remain live at the exchange after the position closes,
	// and checkExits auto-stops the hand on the next tick.
	if isExitOrder {
		h.mu.Lock()
		h.cancelExitOrders(ctx, tradeIDForBracket, symbol, orderID)
		delete(h.exitLevels, tradeIDForBracket)
		h.mu.Unlock()
		return
	}

	// 2. Resolve pendingExits and schedule OCO bracket (entry fills only).
	if !pending.StopLoss.IsPositive() && !pending.TakeProfit.IsPositive() && !pending.IsOffset {
		return
	}
	h.mu.Lock()
	delete(h.pendingExits, orderID) // consume (was set at line ~680 of applyPlaceResult)
	resolvedEl, shouldPlaceBracket, offsetResolved := h.resolvePendingExit(
		ctx, symbol, tradeIDForBracket, pending, fillAvg, restQty, decimal.Zero, decimal.Zero,
	)
	h.mu.Unlock()

	if shouldPlaceBracket {
		h.scheduleBracketPlacement(resolvedEl, symbol, restQty, tradeIDForBracket)
	}

	// 3. Persist resolved offset SL/TP to poslog so restart replay sees the absolute levels
	//    (same KindSLUpdated emitted by the normal applyFill path at step 4).
	if offsetResolved && tradeIDForBracket != "" &&
		(resolvedEl.StopLoss.IsPositive() || resolvedEl.TakeProfit.IsPositive()) {
		var newSL, newTP string
		if resolvedEl.StopLoss.IsPositive() {
			newSL = resolvedEl.StopLoss.String()
		}
		if resolvedEl.TakeProfit.IsPositive() {
			newTP = resolvedEl.TakeProfit.String()
		}
		payload, _ := json.Marshal(poslog.SLUpdatedPayload{
			OrderID: tradeIDForBracket,
			NewSL:   newSL,
			NewTP:   newTP,
			Reason:  "offset_resolved",
		})
		h.publishAndApply(ctx, poslog.Event{
			ID:      tradeIDForBracket + "_sl_resolved_" + orderID,
			HandID:  h.id.String(),
			HelmID:  h.helmID.String(),
			TradeID: tradeIDForBracket,
			Kind:    poslog.KindSLUpdated,
			Payload: payload,
			At:      time.Now().UTC(),
		})
	}
}
