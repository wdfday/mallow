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
	if err := streamer.StreamOrders(ctx, rt.Creds, rt.EnqueueOrderEvent); err != nil {
		slog.Error("order stream start failed", "helm_id", rt.HelmID, "err", err)
		return
	}
	// Dedicated goroutine drains orderCh and processes all event types.
	go r.runOrderProcessor(ctx, nc, rt)
	slog.Info("order streaming started", "helm_id", rt.HelmID, "exchange", rt.Exchange.Name())
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

	// For a full fill on a tracked order: route to the owning hand.
	// The hand's applyFill owns the complete fill lifecycle — portfolio update,
	// exit-level management, metrics, and poslog — so we do NOT call ReportFill here.
	//
	// For partial fills or orders with no owning hand (manual orders placed outside
	// the bot), update the portfolio directly from the registry since there is no
	// hand run-loop to process the fill.
	if ev.Type == exchange.OrderEventFilled && botID != "" {
		rt.mu.RLock()
		hand, ok := rt.hands[botID]
		rt.mu.RUnlock()
		if ok {
			hand.EnqueueFill(ev)
		} else {
			// Hand was removed between order placement and fill — update portfolio directly.
			rt.ReportFill(helmdomain.FillReport{
				HandID:    botID,
				HelmID:    helmID,
				OrderID:   ev.OrderID,
				Symbol:    ev.Symbol,
				Side:      string(ev.Side),
				Qty:       ev.FilledQty,
				Price:     ev.FilledAvg,
				Timestamp: ev.Timestamp,
			})
		}
	} else {
		// Partial fill or manual order: update portfolio from registry.
		// For partial fills with a known owning hand, mark the orderID as seen so
		// pollOrders does not double-apply the same fill via the dust-cancel path.
		if botID != "" && ev.Type == exchange.OrderEventPartialFill {
			rt.mu.RLock()
			hand, ok := rt.hands[botID]
			rt.mu.RUnlock()
			if ok {
				hand.MarkFillSeen(ev.OrderID)
			}
		}
		rt.ReportFill(helmdomain.FillReport{
			HandID:    botID,
			HelmID:    helmID,
			OrderID:   ev.OrderID,
			Symbol:    ev.Symbol,
			Side:      string(ev.Side),
			Qty:       ev.FilledQty,
			Price:     ev.FilledAvg,
			Timestamp: ev.Timestamp,
		})
	}

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
