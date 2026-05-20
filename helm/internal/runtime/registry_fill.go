package runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	helmdomain "mallow/helm/internal/module/helm/domain"
)

// StartFillStreaming starts account fill listeners for all runtimes whose exchange
// implements AccountStreamer. Called once after SpawnAll, from the app lifecycle.
func (r *Registry) StartFillStreaming(ctx context.Context, nc *nats.Conn) {
	r.mu.RLock()
	rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		r.startFillStream(ctx, nc, rt)
	}
}

func (r *Registry) startFillStream(ctx context.Context, nc *nats.Conn, rt *HelmRuntime) {
	streamer, ok := rt.Exchange.(exchange.AccountStreamer)
	if !ok {
		return
	}
	// WS callback only enqueues — never blocks on NATS.
	if err := streamer.StreamOrders(ctx, rt.Creds, rt.EnqueueOrderEvent, makeBalanceHandler(rt)); err != nil {
		slog.Error("order stream start failed", "helm_id", rt.HelmID, "err", err)
		return
	}
	// Dedicated goroutine drains orderCh and processes all event types.
	go r.runOrderProcessor(ctx, nc, rt)
	slog.Info("order streaming started", "helm_id", rt.HelmID, "exchange", rt.Exchange.Name())
}

// makeBalanceHandler returns a callback that syncs the portfolio's cash from a
// live balance push. Only USDT and USDC are treated as quote currency (cash);
// other assets (BTC, ETH, etc.) represent base-currency positions and are ignored.
func makeBalanceHandler(rt *HelmRuntime) func(exchange.BalanceEvent) {
	return func(ev exchange.BalanceEvent) {
		if ev.Asset != "USDT" {
			return
		}
		rt.Portfolio.SyncCash(ev.Free)
		slog.Info("runtime: cash synced from exchange",
			"helm_id", rt.HelmID,
			"asset", ev.Asset,
			"free", ev.Free,
		)
	}
}

// runOrderProcessor drains rt.orderCh and dispatches each event by type:
//   - live:         track manual orders that bypassed hand PlaceOrder
//   - partial_fill / filled: apply fill to portfolio + publish to NATS
//   - canceled:     remove from orderbook
func (r *Registry) runOrderProcessor(ctx context.Context, nc *nats.Conn, rt *HelmRuntime) {
	for {
		select {
		case ev := <-rt.orderCh:
			r.mu.RLock()
			js := r.js
			r.mu.RUnlock()
			switch ev.Type {
			case exchange.OrderEventLive:
				// Dedup: hand orders are already tracked via PlaceOrder REST response.
				// Only track if missing — indicates a manual order placed outside the bot.
				if !rt.HasOrderTracking(ev.OrderID) {
					rt.TrackOrder(ev.OrderID, "manual")
					slog.Info("order tracking: manual order tracked via WS",
						"helm_id", rt.HelmID,
						"order_id", ev.OrderID,
						"symbol", ev.Symbol,
						"qty", ev.Qty,
					)
				}
			case exchange.OrderEventPartialFill, exchange.OrderEventFilled:
				r.applyFill(nc, js, rt, ev)
			case exchange.OrderEventCanceled:
				rt.RemoveOrderTracking(ev.OrderID)
				slog.Info("order tracking: canceled order removed",
					"helm_id", rt.HelmID,
					"order_id", ev.OrderID,
				)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (r *Registry) applyFill(nc *nats.Conn, js nats.JetStreamContext, rt *HelmRuntime, ev exchange.OrderEvent) {
	helmID := rt.HelmID.String()

	// Resolve botID from the order tracking map before the fill removes the record.
	botID := rt.PendingOrderHandID(ev.OrderID)

	slog.Info("exchange: fill processing",
		"helm_id", rt.HelmID,
		"hand_id", botID,
		"order_id", ev.OrderID,
		"trade_id", ev.TradeID,
		"symbol", ev.Symbol,
		"side", ev.Side,
		"fill_qty", ev.FilledQty,
		"fill_avg", ev.FilledAvg,
		"type", ev.Type,
		"exchange_ts", ev.Timestamp,
		"processing_lag", time.Since(ev.Timestamp).Truncate(time.Millisecond),
	)

	rt.MarkTradeProcessed(ev.TradeID)

	fillReport := helmdomain.FillReport{
		HandID:    botID,
		HelmID:    helmID,
		OrderID:   ev.OrderID,
		Symbol:    ev.Symbol,
		Side:      string(ev.Side),
		Qty:       ev.FilledQty,
		Price:     ev.FilledAvg,
		Timestamp: ev.Timestamp,
	}

	if ev.Type == exchange.OrderEventFilled {
		// Full fill is terminal — routing info no longer needed.
		// Remove before dispatching so any concurrent lookup correctly misses.
		rt.RemoveOrderTracking(ev.OrderID)

		if botID != "" {
			rt.mu.RLock()
			hand, ok := rt.hands[botID]
			rt.mu.RUnlock()
			if ok {
				// Route to hand: hand.applyFill owns exit-level management,
				// poslog, metrics, and calls rt.ReportFill itself.
				// Publish trade.filled here before routing — hand processes
				// asynchronously via fillCh so we can't rely on it to publish.
				hand.EnqueueFill(ev)
				if js != nil {
					natsapi.PublishTradeFill(js, natsapi.TransactionMsg{
						HelmID:    helmID,
						AccountID: rt.AccountID.String(),
						UserID:    rt.UserID.String(),
						HandID:    botID,
						TradeID:   ev.TradeID,
						OrderID:   ev.OrderID,
						Kind:      "fill",
						Symbol:    ev.Symbol,
						Side:      string(ev.Side),
						Qty:       ev.FilledQty,
						AvgPrice:  ev.FilledAvg,
						FilledAt:  ev.Timestamp,
					})
				}
				return
			}
		}
		// Orphan: hand removed between order placement and fill.
		// Fall through to ReportFill to at least update portfolio.
		rt.ReportFill(fillReport)
	} else {
		// Partial fill: order is still open — keep routing info for subsequent fills.
		// Update portfolio with the filled portion, but routing stays intact.
		if botID != "" {
			rt.mu.RLock()
			hand, ok := rt.hands[botID]
			rt.mu.RUnlock()
			if ok {
				// Mark seen so pollOrders (5s REST tick) does not double-apply this partial.
				hand.MarkFillSeen(ev.OrderID)
			}
		}
		rt.ReportFill(fillReport)
	}

	// Emit fill event for orphan and partial paths — hand emits its own CodeOrderFilled
	// on the normal path (EnqueueFill → applyFill → EmitEvent). These two paths have
	// no hand, so we emit here to keep HELM_EVENTS consistent.
	rt.EmitEvent(natsapi.HelmEvent{
		HandID:  botID,
		Code:    CodeOrderFilled,
		Symbol:  ev.Symbol,
		Side:    string(ev.Side),
		Qty:     ev.FilledQty,
		Price:   ev.FilledAvg,
		OrderID: ev.OrderID,
		Msg:     "runtime: fill applied to portfolio",
	})

	if js == nil {
		return
	}
	natsapi.PublishTradeFill(js, natsapi.TransactionMsg{
		HelmID:    rt.HelmID.String(),
		AccountID: rt.AccountID.String(),
		UserID:    rt.UserID.String(),
		HandID:    botID,
		TradeID:   ev.TradeID,
		OrderID:   ev.OrderID,
		Kind:      "fill",
		Symbol:    ev.Symbol,
		Side:      string(ev.Side),
		Qty:       ev.FilledQty,
		AvgPrice:  ev.FilledAvg,
		FilledAt:  ev.Timestamp,
	})
}
