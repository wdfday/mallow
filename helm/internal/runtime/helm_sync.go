package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/runtime/core/portfolio"
)

// Fill idempotency delegates to fillDedup (see helpers.go).

// MarkOrderFillPublished records an orderID whose trade.filled was already published
// via the WS fill path. Subsequent calls to Sync() skip transactions with this orderID
// to prevent double-publishing the same fill with a different Nats-Msg-Id.
func (r *HelmRuntime) MarkOrderFillPublished(orderID string) { r.dedup.markFillPublished(orderID) }

// hasOrderFillPublished returns true if trade.filled was already published for this orderID
// via the REST fill path.
func (r *HelmRuntime) hasOrderFillPublished(orderID string) bool {
	return r.dedup.hasFillPublished(orderID)
}

// HasProcessedTrade returns true if this TradeID was already applied in the current session.
func (r *HelmRuntime) HasProcessedTrade(tradeID string) bool { return r.dedup.hasTrade(tradeID) }

// MarkTradeProcessed records a TradeID so duplicate gap recovery fills are skipped.
func (r *HelmRuntime) MarkTradeProcessed(tradeID string) { r.dedup.markTrade(tradeID) }

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

// persistSyncTime writes the runtime's lastSyncAt to the store (best-effort, logs on error).
func (r *HelmRuntime) persistSyncTime() {
	if r.syncStore == nil {
		return
	}
	if err := r.syncStore.UpdateLastSyncedAt(r.HelmID, r.LastSyncAt()); err != nil {
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
			Symbol:   p.Symbol,
			Qty:      p.Qty,
			AvgPrice: p.AvgPrice,
			CurPrice: p.CurPrice,
		}
		natsPositions[i] = natsapi.SyncedPositionMsg{
			Symbol:   p.Symbol,
			Qty:      p.Qty,
			AvgPrice: p.AvgPrice,
			CurPrice: p.CurPrice,
		}
	}
	r.Portfolio.ApplySync(snap.Cash, pfPositions)

	for _, p := range snap.Positions {
		if p.CurPrice.IsPositive() {
			r.prices.set(p.Symbol, p.CurPrice)
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
		handID := r.PendingOrderHandID(canonOrderKey(t.ClientOrderID, t.OrderID))
		msg := natsapi.TransactionMsg{
			HelmID:    helmID,
			AccountID: accountID,
			UserID:    userID,
			HandID:    handID,
			TradeID:   t.TradeID,
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
	return nil
}

// ReconcileOrders fetches open orders from the exchange and re-tracks any that are
// missing from the in-memory orderbook (lost across restarts).
// Call after SpawnAll + StartFillStreaming so the fill processor is ready.
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
		key := canonOrderKey(o.ClientOrderID, o.ID)
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
// Call after ReconcileOrders but before StartFillStreaming.
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
		if r.HasProcessedTrade(txn.TradeID) {
			continue
		}
		r.applyWsFill(exchange.WsFillEvent{
			OrderID:         txn.OrderID,
			TradeID:         txn.TradeID,
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
