package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"

	"orchestrator/internal/infra/exchange"
	"orchestrator/internal/infra/natsapi"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
	"orchestrator/internal/runtime/core/orderbook"
)

// StartFillStreaming starts account fill listeners for all runtimes whose exchange
// implements AccountStreamer. Called once after SpawnAll, from the app lifecycle.
func (r *Registry) StartFillStreaming(ctx context.Context, nc *nats.Conn) {
	r.mu.RLock()
	rts := make([]*Orchestrator, 0, len(r.runtimes))
	for _, rt := range r.runtimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		r.startFillStream(ctx, nc, rt)
	}
}

func (r *Registry) startFillStream(ctx context.Context, nc *nats.Conn, rt *Orchestrator) {
	streamer, ok := rt.Exchange.(exchange.AccountStreamer)
	if !ok {
		return
	}
	// WS callback only enqueues — never blocks on NATS.
	if err := streamer.StreamOrders(ctx, rt.Creds, rt.EnqueueOrderEvent); err != nil {
		slog.Error("order stream start failed", "orchestrator_id", rt.OrchestratorID, "err", err)
		return
	}
	// Dedicated goroutine drains orderCh and processes all event types.
	go r.runOrderProcessor(ctx, nc, rt)
	slog.Info("order streaming started", "orchestrator_id", rt.OrchestratorID, "exchange", rt.Exchange.Name())
}

// runOrderProcessor drains rt.orderCh and dispatches each event by type:
//   - live:         track manual orders that bypassed bot PlaceOrder
//   - partial_fill / filled: apply fill to portfolio + publish to NATS
//   - canceled:     remove from orderbook
func (r *Registry) runOrderProcessor(ctx context.Context, nc *nats.Conn, rt *Orchestrator) {
	for {
		select {
		case ev := <-rt.orderCh:
			r.mu.RLock()
			js := r.js
			r.mu.RUnlock()
			orchID := rt.OrchestratorID.String()
			switch ev.Type {
			case exchange.OrderEventLive:
				// Dedup: bot orders are already tracked via PlaceOrder REST response.
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
						"orchestrator_id", rt.OrchestratorID,
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
					"orchestrator_id", rt.OrchestratorID,
					"order_id", ev.OrderID,
				)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (r *Registry) applyFill(nc *nats.Conn, js nats.JetStreamContext, rt *Orchestrator, ev exchange.OrderEvent) {
	orchID := rt.OrchestratorID.String()

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
	subj := fmt.Sprintf(natsapi.SubjTradeFilled, rt.OrchestratorID)
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

	// Publish to investment JetStream for event sourcing. Dedup via Nats-Msg-Id.
	if js != nil {
		txn := natsapi.TransactionMsg{
			OrderID:  ev.OrderID,
			Symbol:   ev.Symbol,
			Side:     string(ev.Side),
			Qty:      ev.FilledQty,
			AvgPrice: ev.FilledAvg,
			FilledAt: ev.Timestamp,
		}
		natsapi.PublishInvestmentTransaction(js, rt.OrchestratorID.String(), rt.AccountID.String(), rt.UserID.String(), botID, rt.BrokerType, txn)
	}
}
