package actor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor/clid"
	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/position"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
)

// Fill idempotency delegates to fillDedup

// MarkOrderFillPublished records an orderID whose trade.filled was already published
// via the WS fill path. Subsequent calls to Sync() skip transactions with this orderID
// to prevent double-publishing the same fill with a different Nats-Msg-Id.
func (r *HelmRuntime) MarkOrderFillPublished(orderID string) { r.dedup.markFillPublished(orderID) }

// hasOrderFillPublished returns true if trade.filled was already published for this orderID
// via the REST fill path.
func (r *HelmRuntime) hasOrderFillPublished(orderID string) bool {
	return r.dedup.hasFillPublished(orderID)
}

// HasProcessedFill returns true if this FillID was already applied in the current session.
func (r *HelmRuntime) HasProcessedFill(fillID string) bool { return r.dedup.hasFillID(fillID) }

// MarkFillProcessed records a FillID so duplicate gap recovery fills are skipped.
func (r *HelmRuntime) MarkFillProcessed(fillID string) { r.dedup.markFillID(fillID) }

// LastSyncAt returns the timestamp of the most recent portfolio sync, or zero if never synced.
func (r *HelmRuntime) LastSyncAt() time.Time {
	ns := r.lastSyncAtNano.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

func (r *HelmRuntime) storeSyncAt(t time.Time) {
	ns := t.UnixNano()
	for {
		cur := r.lastSyncAtNano.Load()
		if ns <= cur {
			return
		}
		if r.lastSyncAtNano.CompareAndSwap(cur, ns) {
			return
		}
	}
}

// SyncStore persists the last successful sync timestamp for crash recovery.
// Implemented by domain.HelmRepo; wired in by Registry (package fleet).
type SyncStore interface {
	UpdateLastSyncedAt(id uuid.UUID, t time.Time) error
}

// PersistSyncTime writes the runtime's lastSyncAt to the store (best-effort, logs on error).
// Exported: called from Registry's polling-sync loop (package fleet) as well as internally.
func (r *HelmRuntime) PersistSyncTime() {
	if r.SyncStore == nil {
		return
	}
	if err := r.SyncStore.UpdateLastSyncedAt(r.HelmID, r.LastSyncAt()); err != nil {
		slog.Warn("runtime: persist last_synced_at failed", "helm_id", r.HelmID, "err", err)
	}
}

// Sync fetches current account state from the exchange REST API, updates the
// in-memory portfolio, and publishes a portfolio.synced event to NATS.
func (r *HelmRuntime) Sync(ctx context.Context) error {
	syncer, ok := r.Exchange.(exchange.AccountSyncer)
	if !ok {
		return nil
	}
	var since *time.Time
	if prev := r.LastSyncAt(); !prev.IsZero() {
		since = &prev
	}
	snap, err := syncer.SyncAccount(ctx, r.Creds, since)
	if err != nil {
		return err
	}

	pfPositions := make([]portfolio.SyncedPosition, len(snap.Positions))
	natsPositions := make([]natsapi.SyncedPositionMsg, len(snap.Positions))
	for i, p := range snap.Positions {
		pfPositions[i] = portfolio.SyncedPosition{
			Symbol:           p.Symbol,
			Qty:              p.Qty,
			AvgPrice:         p.AvgPrice,
			CurPrice:         p.CurPrice,
			Side:             string(p.Side),
			Leverage:         p.Leverage,
			LiquidationPrice: p.LiquidationPrice,
			MarginMode:       p.MarginMode,
		}
		natsPositions[i] = natsapi.SyncedPositionMsg{
			Symbol:   p.Symbol,
			Qty:      p.Qty,
			AvgPrice: p.AvgPrice,
			CurPrice: p.CurPrice,
		}
	}
	pfBalances := make([]portfolio.Balance, len(snap.Balances))
	for i, b := range snap.Balances {
		pfBalances[i] = portfolio.Balance{
			Asset:         b.Asset,
			Free:          b.Free,
			Locked:        b.Locked,
			MarginBalance: b.MarginBalance,
			UnrealizedPnL: b.UnrealizedPnL,
		}
	}
	r.Portfolio.ApplySync(snap.Cash, pfPositions, snap.AccountEquity, pfBalances, snap.MarginRatio)
	r.checkTradingRestricted(snap.Permissions)

	for _, p := range snap.Positions {
		if p.CurPrice.IsPositive() {
			r.MarketData.SetPrice(p.Symbol, p.CurPrice)
		}
	}

	prevSyncAt := r.LastSyncAt()

	natsPositionTxns := make([]natsapi.TransactionMsg, 0, len(snap.Transactions))
	var newTxns []natsapi.TransactionMsg
	helmID := r.HelmID.String()
	accountID := r.AccountID.String()
	userID := r.UserID.String()
	for _, t := range snap.Transactions {
		// Best-effort: resolve which hand placed this order via its clid (falls back to
		// exchange id when the venue's trade record omits the clOrdId, e.g. Binance).
		// Works only while the order is still tracked in orderHandMap (slow/gap fills);
		// returns "" for orders already cleared — omitempty hides it from JSON.
		handID := r.PendingOrderHandID(clid.CanonKey(t.ClientOrderID, t.OrderID))
		msg := natsapi.TransactionMsg{
			HelmID:    helmID,
			AccountID: accountID,
			UserID:    userID,
			HandID:    handID,
			TradeID:   t.FillID,
			OrderID:   t.OrderID,
			Kind:      "fill",
			Symbol:    t.Symbol,
			Side:      t.Side,
			Qty:       t.Qty,
			AvgPrice:  t.AvgPrice,
			Fee:       t.Fee,
			FilledAt:  t.FilledAt,
		}
		natsPositionTxns = append(natsPositionTxns, msg)
		if prevSyncAt.IsZero() || t.FilledAt.After(prevSyncAt) {
			newTxns = append(newTxns, msg)
		}
	}

	now := time.Now().UTC()
	r.storeSyncAt(now)

	r.EmitEvent(natsapi.HelmEvent{
		Code:   CodeHelmSynced,
		Reason: fmt.Sprintf("positions=%d new_txns=%d", len(pfPositions), len(newTxns)),
		Msg:    "helm: portfolio synced from exchange",
	})

	if r.js != nil {
		natsapi.PublishPortfolioSync(r.js, helmID, accountID, userID, snap.Cash, r.AvailableCash(), snap.Equity, natsPositions, natsPositionTxns, now)
		for _, t := range newTxns {
			// Skip fills already published by the REST fill path to prevent duplicates.
			if r.hasOrderFillPublished(t.OrderID) {
				continue
			}
			natsapi.PublishTradeFill(r.js, t)
		}
	}

	// Trigger the desync check at Helm level
	r.checkPositionDesync(ctx)

	return nil
}

// ReconcileOrders fetches open orders from the exchange and re-tracks any that are
// missing from the in-memory orderbook (lost across restarts).
// Call after SpawnAll + StartStreaming so the fill processor is ready.
func (r *HelmRuntime) ReconcileOrders(ctx context.Context) {
	reconciler, ok := r.Exchange.(exchange.OrderReconciler)
	if !ok {
		return
	}
	orders, err := reconciler.GetPendingOrders(ctx, r.Creds, "")
	if err != nil {
		slog.Warn("reconcile orders: fetch failed", "helm_id", r.HelmID, "err", err)
		return
	}
	if len(orders) == 0 {
		return
	}

	recovered := 0
	for _, o := range orders {
		// Track by clid when the exchange echoes ours (race-free key shared with WS fills),
		// else by exchange id. handID unknown after crash — fill routing falls back to REST poll.
		key := clid.CanonKey(o.ClientOrderID, o.ID)
		if r.HasOrderTracking(key) {
			continue
		}
		r.TrackOrder(key, "")
		recovered++
	}
	if recovered > 0 {
		slog.Info("reconcile orders: recovered pending orders",
			"helm_id", r.HelmID,
			"recovered", recovered,
			"total_open", len(orders))
	}
}

// RecoverGapFills fetches filled order history from the exchange covering the window
// [since, now), where since = LastSyncAt (crash recovery) or CreatedAt (first boot).
// Applies fills missed during downtime so portfolio state is correct on restart.
// Call after ReconcileOrders but before StartStreaming.
func (r *HelmRuntime) RecoverGapFills(ctx context.Context) {
	historian, ok := r.Exchange.(exchange.HistoryFetcher)
	if !ok {
		return
	}

	var since time.Time
	if lastSync := r.LastSyncAt(); !lastSync.IsZero() {
		since = lastSync
	} else if !r.CreatedAt.IsZero() {
		since = r.CreatedAt
	} else {
		slog.Warn("gap recovery: skipping helm with no createdAt or lastSyncAt",
			"helm_id", r.HelmID)
		return
	}

	now := time.Now().UTC()
	if now.Sub(since) < 5*time.Second {
		return // restarted too recently, nothing to recover
	}

	slog.Info("gap recovery: fetching fill history",
		"helm_id", r.HelmID, "from", since, "to", now)

	// Pass nil symbols — fetch all instruments.
	fills, err := historian.FilledOrders(ctx, r.Creds, nil, since, now)
	if err != nil {
		slog.Error("gap recovery: fetch failed", "helm_id", r.HelmID, "err", err)
		return
	}
	if len(fills) == 0 {
		slog.Info("gap recovery: no fills in gap", "helm_id", r.HelmID)
		return
	}

	applied := 0
	for _, txn := range fills {
		if r.HasProcessedFill(txn.FillID) {
			continue
		}
		r.applyWsFill(ctx, exchange.WsFillEvent{
			OrderID:         txn.OrderID,
			FillID:          txn.FillID,
			Symbol:          txn.Symbol,
			Side:            exchange.OrderSide(txn.Side),
			Partial:         false,
			FilledQty:       txn.Qty,
			FilledAvg:       txn.AvgPrice,
			Commission:      txn.Fee,
			CommissionAsset: txn.FeeAsset,
			Timestamp:       txn.FilledAt,
		})
		applied++
	}
	slog.Info("gap recovery: fills applied",
		"helm_id", r.HelmID, "total", len(fills), "applied", applied, "skipped", len(fills)-applied)
}

// RecoverHandBrackets re-places missing OCO bracket orders for all restored hands.
// Must be called AFTER RecoverGapFills so that bracket fills that occurred during
// downtime are already applied — otherwise we would re-place a bracket for a position
// that is already closed.
//
// A bracket is considered missing when exitLevels[tradeID].ExchangeOrderIDs is empty
// but SL/TP levels are known from poslog (KindSLUpdated was persisted but
// KindBracketPlaced was not — crash window between the two events).
func (r *HelmRuntime) RecoverHandBrackets(ctx context.Context) {
	r.mu.RLock()
	hands := make([]*Hand, 0, len(r.hands))
	for _, e := range r.hands {
		hands = append(hands, e.h)
	}
	r.mu.RUnlock()
	for _, h := range hands {
		// Restore exitLevels from poslog-backed position state BEFORE running
		// RecoverBrackets. exitLevels is ephemeral (not persisted) — on restart it
		// starts empty, causing RecoverBrackets to find nothing and checkPositionDesync
		// to mark the position as unprotected → spurious EXT_CLOSE.
		h.restoreExitLevelsFromPoslog()
		h.RecoverBrackets(ctx)
	}
}

// checkPositionDesync scans all hands for active legs and compares them against
// the authoritative exchange portfolio to detect external close events.
// It is called after a successful REST Sync.
func (r *HelmRuntime) checkPositionDesync(ctx context.Context) {
	r.mu.RLock()
	hands := make([]*Hand, 0, len(r.hands))
	for _, e := range r.hands {
		hands = append(hands, e.h)
	}
	r.mu.RUnlock()

	// 1. Group active open legs by symbol.
	type handLeg struct {
		hand *Hand
		leg  *position.LegState
	}
	symbolLegs := make(map[string][]handLeg)
	for _, hand := range hands {
		hand.mu.RLock()
		legs := hand.pos.ActiveLegs()
		hand.mu.RUnlock()

		for _, leg := range legs {
			if leg.Phase == position.PhaseOpen {
				symbolLegs[leg.Symbol] = append(symbolLegs[leg.Symbol], handLeg{hand: hand, leg: leg})
			}
		}
	}

	// 2. Perform desync check for each symbol.
	for symbol, hlList := range symbolLegs {
		// Get authoritative position from portfolio.
		pos := r.Portfolio.GetPosition(symbol)
		exchangeQty := decimal.Zero
		if pos != nil {
			exchangeQty = pos.Qty.Abs()
		}

		// Separate into protected and unprotected groups.
		//
		// Protected: any leg that has SL/TP levels configured — regardless of whether
		// ExchangeOrderIDs are known. If levels are set but IDs are empty (e.g. helm
		// crashed between PlaceExitOrders and KindBracketPlaced), RecoverBrackets or
		// applyBracketStates will re-establish the bracket. The hand manages the close
		// via OCO tracking; desync detection must not fire EXT_CLOSE for these legs.
		//
		// Unprotected: legs with NO exit levels at all (no SL/TP, no bracket). Only
		// these are eligible for EXT_CLOSE via portfolio desync.
		var protected []handLeg
		var unprotected []handLeg
		for _, hl := range hlList {
			hl.hand.mu.RLock()
			el, hasExit := hl.hand.exitLevels[hl.leg.TradeID]
			hl.hand.mu.RUnlock()

			if hasExit && (len(el.ExchangeOrderIDs) > 0 || el.StopLoss.IsPositive() || el.TakeProfit.IsPositive()) {
				protected = append(protected, hl)
			} else {
				unprotected = append(unprotected, hl)
			}
		}

		// Subtract protected leg quantities from exchangeQty.
		for _, hl := range protected {
			exchangeQty = exchangeQty.Sub(hl.leg.Qty.Abs())
		}
		if exchangeQty.IsNegative() {
			exchangeQty = decimal.Zero
		}

		// Check unprotected legs against remaining exchange quantity.
		for _, hl := range unprotected {
			legQty := hl.leg.Qty.Abs()
			if exchangeQty.GreaterThanOrEqual(legQty) {
				exchangeQty = exchangeQty.Sub(legQty)
			} else {
				// Deficit detected -> external close suspected for this hand's leg.
				// Delivered as a signal (not a direct cross-goroutine method call) so
				// the state mutation happens on the hand's own run loop, same as every
				// other trigger in this file — see handleOrphanSignal/handlePositionDesync.
				hl.hand.DeliverSignal(Signal{
					Symbol:     hl.leg.Symbol,
					Direction:  strategy.DirExit,
					ExitKind:   strategy.ExitKindOrphan,
					Strength:   1.0,
					ReceivedAt: time.Now(),
				})
			}
		}
	}
}

// KNOWN GAP (2026-07-10, not fixed): when total hand-tracked qty for a symbol exceeds
// the real exchange qty (multi-hand-same-symbol desync — see the RemovePosition /
// Portfolio.positions notes elsewhere in this codebase for how that state can arise
// in the first place), there is no principled policy for which hand's leg absorbs the
// shortfall. Here, it's whichever hand happens first in `unprotected`'s iteration
// order — which traces back to ranging over r.hands (a Go map, deliberately
// randomized iteration by the language spec) at the top of this function. Two hands
// in the exact same real desync get two different outcomes purely by map-iteration
// luck: the first exhausts the remaining exchangeQty and passes; the next one checked
// finds it empty and gets orphaned.
//
// The same absence of policy shows up a second way, at a different layer: the
// freeQty.LessThan(enough) balance-check in runPlaceREST (hand_runner.go) runs
// independently per hand, off-loop, when each hand's own exit attempt happens to
// reach the exchange. If two hands both try to exit into a real shortfall at close to
// the same time, whichever hand's REST round-trip lands first consumes the free
// balance and exits cleanly; the slower one's check then finds it gone and orphans.
// Same failure shape (no allocation policy under scarcity), different trigger
// (periodic map-order race here vs. live network-timing race there).
//
// Not fixed — a real fix means picking and implementing an actual policy (proportional
// split across all affected legs? oldest-position-first? something else?), not a
// one-line change, and every case the two paths above touch already got real edits
// tonight. Worth resolving both together, once a policy is chosen, not patched twice
// in the same style that produced them.
//
// Whatever the eventual policy, the state this reacts to — total hand-tracked qty for
// a symbol not matching real exchange qty — still traces back to something outside
// helm's own control in every case this codebase has actually observed tonight
// (see the earlier "chắc chắn do user" thread): multi-hand overlap the user configured,
// or the user's own hand at the exchange. Not a helm accounting bug on its own — just
// one without a fair answer yet for who eats it when it happens.
