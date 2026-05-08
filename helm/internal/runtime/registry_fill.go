package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	orchdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/core/orderbook"
)

// StartFillStreaming starts account fill listeners for all runtimes whose exchange
// implements AccountStreamer. Called once after SpawnAll, from the app lifecycle.
func (r *Registry) StartFillStreaming(ctx context.Context, nc *nats.Conn) {
	r.mu.RLock()
	rts := make([]*HelmRuntime, 0, len(r.runtimes))
	for _, rt := range r.runtimes {
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
			orchID := rt.HelmID.String()
			switch ev.Type {
			case exchange.OrderEventLive:
				// Dedup: hand orders are already tracked via PlaceOrder REST response.
				// Only track if missing — indicates a manual order placed outside the bot.
				if !rt.OrderBook.Has(orchID, ev.OrderID) {
					rt.OrderBook.TrackOrder(orderbook.PendingOrder{
						OrchestratorID: orchID,
						OrderID:        ev.OrderID,
						BotID:          "manual",
						Symbol:         ev.Symbol,
						Side:           orderbook.OrderSide(ev.Side),
						Qty:            ev.Qty,
					})
					slog.Info("order book: manual order tracked via WS",
						"helm_id", rt.HelmID,
						"order_id", ev.OrderID,
						"symbol", ev.Symbol,
						"qty", ev.Qty,
					)
				}
			case exchange.OrderEventPartialFill, exchange.OrderEventFilled:
				r.applyFill(nc, js, rt, ev)
			case exchange.OrderEventCanceled:
				rt.OrderBook.RemoveOrder(orchID, ev.OrderID)
				slog.Info("order book: canceled order removed",
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
	orchID := rt.HelmID.String()

	// Resolve botID from pending orders before the fill removes the record.
	botID := ""
	for _, p := range rt.OrderBook.PendingOrders(orchID) {
		if p.OrderID == ev.OrderID {
			botID = p.BotID
			break
		}
	}

	rt.ReportFill(orchdomain.FillReport{
		BotID:          botID,
		OrchestratorID: orchID,
		OrderID:        ev.OrderID,
		Symbol:         ev.Symbol,
		Side:           string(ev.Side),
		Qty:            ev.FilledQty,
		Price:          ev.FilledAvg,
		Timestamp:      ev.Timestamp,
	})

	if nc == nil {
		return
	}
	subj := fmt.Sprintf(natsapi.SubjTradeFilled, rt.HelmID)
	data, _ := json.Marshal(natsapi.FillNotification{
		OrchestratorID: orchID,
		BotID:          botID,
		OrderID:        ev.OrderID,
		Symbol:         ev.Symbol,
		Side:           string(ev.Side),
		FilledQty:      ev.FilledQty,
		FilledAvg:      ev.FilledAvg,
		Timestamp:      ev.Timestamp,
	})
	if err := nc.Publish(subj, data); err != nil {
		slog.Warn("fill: nats publish failed", "subject", subj, "err", err)
	}

	// Publish to investment JetStream for event sourcing. Dedup via Nats-Msg-Id = TradeID.
	if js != nil {
		txn := natsapi.TransactionMsg{
			TradeID:  ev.TradeID,
			OrderID:  ev.OrderID,
			Kind:     "fill",
			Symbol:   ev.Symbol,
			Side:     string(ev.Side),
			Qty:      ev.FilledQty,
			AvgPrice: ev.FilledAvg,
			FilledAt: ev.Timestamp,
		}
		natsapi.PublishInvestmentTransaction(js, rt.HelmID.String(), rt.AccountID.String(), rt.UserID.String(), botID, rt.BrokerType, txn)
	}
}
