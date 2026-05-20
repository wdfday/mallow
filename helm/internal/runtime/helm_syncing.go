package runtime

import (
	"context"
	"fmt"
	"time"

	nats "github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/runtime/core/portfolio"
)

// HasProcessedTrade returns true if this TradeID was already applied in the current session.
func (r *HelmRuntime) HasProcessedTrade(tradeID string) bool {
	if tradeID == "" {
		return false
	}
	r.processedTradesMu.Lock()
	_, ok := r.processedTrades[tradeID]
	r.processedTradesMu.Unlock()
	return ok
}

// MarkTradeProcessed records a TradeID so duplicate gap recovery fills are skipped.
func (r *HelmRuntime) MarkTradeProcessed(tradeID string) {
	if tradeID == "" {
		return
	}
	r.processedTradesMu.Lock()
	r.processedTrades[tradeID] = struct{}{}
	r.processedTradesMu.Unlock()
}

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

// Sync fetches current account state from the exchange REST API, updates the in-memory
// portfolio, and publishes a portfolio.synced event to NATS.
func (r *HelmRuntime) Sync(ctx context.Context, js nats.JetStreamContext) error {
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

	r.pricesMu.Lock()
	for _, p := range snap.Positions {
		if p.CurPrice.IsPositive() {
			r.prices[p.Symbol] = p.CurPrice
		}
	}
	r.pricesMu.Unlock()

	prevSyncAt := r.LastSyncAt()

	natsPositionTxns := make([]natsapi.TransactionMsg, 0, len(snap.Transactions))
	var newTxns []natsapi.TransactionMsg
	helmID := r.HelmID.String()
	accountID := r.AccountID.String()
	userID := r.UserID.String()
	for _, t := range snap.Transactions {
		msg := natsapi.TransactionMsg{
			HelmID:    helmID,
			AccountID: accountID,
			UserID:    userID,
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

	if js != nil {
		natsapi.PublishPortfolioSync(js, helmID, accountID, userID, snap.Cash, snap.Equity, natsPositions, natsPositionTxns, now)
		for _, t := range newTxns {
			natsapi.PublishTradeFill(js, t)
		}
	}
	return nil
}
