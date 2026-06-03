package runtime

// helm_fills.go — WS fill streaming owned by HelmRuntime.
//
// Each runtime manages its own WebSocket order stream, lifecycle event processor,
// and fill processor. Registry is responsible only for starting all runtimes.

import (
	"context"
	"log/slog"
	"time"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/clid"
)

// StartFillStreaming opens the WS order stream and starts the fill and lifecycle
// drain goroutines. No-op if the exchange does not implement AccountStreamer.
// appCtx governs drain goroutine lifetime; a separate per-stream context controls
// the WS connection so it can be replaced on RotateFillStream without stopping drains.
func (r *HelmRuntime) StartFillStreaming(appCtx context.Context) {
	streamCtx, cancel := context.WithCancel(appCtx)
	r.fillStreamMu.Lock()
	r.fillStreamCancel = cancel
	r.fillStreamMu.Unlock()

	if !r.connectStream(streamCtx) {
		cancel()
		return
	}
	go r.runLifecycleProcessor(appCtx)
	go r.runFillProcessor(appCtx)
}

// RotateFillStream replaces credentials and reconnects the WS stream.
// Drain goroutines (runLifecycleProcessor, runFillProcessor) keep running on appCtx —
// only the WS connection itself is torn down and re-opened.
func (r *HelmRuntime) RotateFillStream(appCtx context.Context, newCreds exchange.Credentials) {
	// Cancel current WS stream context.
	r.fillStreamMu.Lock()
	if r.fillStreamCancel != nil {
		r.fillStreamCancel()
		r.fillStreamCancel = nil
	}
	r.fillStreamMu.Unlock()

	// Update credentials immediately so subsequent REST calls use the new key.
	r.mu.Lock()
	r.Creds = newCreds
	r.mu.Unlock()

	// Give the WS adapter goroutines a moment to observe the cancelled context.
	time.Sleep(300 * time.Millisecond)

	// Reconnect WS with new credentials.
	streamCtx, cancel := context.WithCancel(appCtx)
	r.fillStreamMu.Lock()
	r.fillStreamCancel = cancel
	r.fillStreamMu.Unlock()

	if !r.connectStream(streamCtx) {
		cancel()
		slog.Error("rotate creds: WS reconnect failed after key rotation", "helm_id", r.HelmID)
	}
}

// connectStream opens the WS order stream for this runtime's current credentials.
// Returns false if the exchange does not support streaming or if the call fails.
// Does NOT start drain goroutines — that is StartFillStreaming's job.
func (r *HelmRuntime) connectStream(streamCtx context.Context) bool {
	streamer, ok := r.Exchange.(exchange.AccountStreamer)
	if !ok {
		return false
	}
	r.mu.RLock()
	creds := r.Creds
	r.mu.RUnlock()

	if err := streamer.StreamOrders(
		streamCtx,
		creds,
		r.EnqueueLifecycleEvent,
		r.EnqueueWsFill,
		r.handleBalanceEvent,
	); err != nil {
		slog.Error("order stream start failed", "helm_id", r.HelmID, "err", err)
		return false
	}
	slog.Info("order streaming started", "helm_id", r.HelmID, "exchange", r.Exchange.Name())
	return true
}

// handleBalanceEvent syncs portfolio cash from a live balance push.
// Only USDT is treated as quote currency; base assets (BTC, ETH…) are ignored.
func (r *HelmRuntime) handleBalanceEvent(ev exchange.BalanceEvent) {
	if ev.Asset != "USDT" {
		return
	}
	r.Portfolio.SyncCash(ev.Free)
	slog.Info("runtime: cash synced from exchange",
		"helm_id", r.HelmID,
		"asset", ev.Asset,
		"free", ev.Free,
	)
}

// runLifecycleProcessor drains lifecycleCh and dispatches order lifecycle events:
//   - Live:     informational — our orders are already tracked by clid before placement
//   - Canceled: remove from orderbook and notify the owning hand
func (r *HelmRuntime) runLifecycleProcessor(ctx context.Context) {
	for {
		select {
		case ev := <-r.lifecycleCh:
			switch ev.Type {
			case exchange.OrderLifecycleEventLive:
				// Our orders are tracked by clid before PlaceOrder, so no tracking is needed
				// here. Untracked orders (placed manually outside the bot) intentionally stay
				// untracked and resolve to the orphan path when they fill.
				slog.Debug("order: live ack",
					"helm_id", r.HelmID,
					"order_id", ev.OrderID,
					"client_order_id", ev.ClientOrderID,
					"symbol", ev.Symbol,
				)
			case exchange.OrderLifecycleEventCanceled:
				// Lookup hand BEFORE removing tracking — needed for external-close detection.
				key := clid.CanonKey(ev.ClientOrderID, ev.OrderID)
				handID := r.PendingOrderHandID(key)
				r.RemoveOrderTracking(key)
				if key != ev.OrderID {
					r.RemoveOrderTracking(ev.OrderID) // drop the exchange-id alias too
				}
				slog.Info("order tracking: canceled order removed",
					"helm_id", r.HelmID,
					"order_id", ev.OrderID,
				)
				if handID != "" {
					r.mu.RLock()
					hand, ok := r.hands[handID]
					r.mu.RUnlock()
					if ok {
						go hand.HandleExitOrderCanceled(ctx, ev.OrderID)
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// runFillProcessor drains wsFillCh and applies each WS fill event.
func (r *HelmRuntime) runFillProcessor(ctx context.Context) {
	for {
		select {
		case ev := <-r.wsFillCh:
			r.applyWsFill(ev)
		case <-ctx.Done():
			return
		}
	}
}

// applyWsFill processes a single WS fill event.
// Full fills are routed to the owning hand (poslog + metrics + exit logic).
// Partial fills are applied incrementally to the portfolio and tracked for REST dedup.
func (r *HelmRuntime) applyWsFill(ev exchange.WsFillEvent) {
	helmID := r.HelmID.String()

	// Resolve owning hand before removing the tracking entry. canonOrderKey picks the
	// clid for our orders (tracked before placement → no WS-before-REST race) and the
	// exchange id for everything else. An untracked order (placed manually outside the
	// bot) resolves to "" → the orphan path.
	routeKey := clid.CanonKey(ev.ClientOrderID, ev.OrderID)
	botID := r.PendingOrderHandID(routeKey)

	// Rollout observability: record how this fill resolved to a hand.
	// See CLIENT_ORDER_ID.md.
	fillRoute := "orphan"
	switch {
	case botID == "":
		r.fillRouteOrphan.Add(1)
	case routeKey == ev.ClientOrderID && clid.IsOurClid(ev.ClientOrderID):
		r.fillRouteClid.Add(1)
		fillRoute = "clid"
	default:
		r.fillRouteAlias.Add(1)
		fillRoute = "alias"
	}

	slog.Info("exchange: fill processing",
		"helm_id", r.HelmID,
		"hand_id", botID,
		"order_id", ev.OrderID,
		"client_order_id", ev.ClientOrderID,
		"fill_route", fillRoute,
		"trade_id", ev.TradeID,
		"symbol", ev.Symbol,
		"side", ev.Side,
		"fill_qty", ev.FilledQty,
		"fill_avg", ev.FilledAvg,
		"partial", ev.Partial,
		"exchange_ts", ev.Timestamp,
		"processing_lag", time.Since(ev.Timestamp).Truncate(time.Millisecond),
	)

	r.MarkTradeProcessed(ev.TradeID)

	fillReport := helmdomain.FillReport{
		HandID:     botID,
		HelmID:     helmID,
		OrderID:    ev.OrderID,
		Symbol:     ev.Symbol,
		Side:       string(ev.Side),
		Qty:        ev.FilledQty,
		Price:      ev.FilledAvg,
		Commission: ev.Commission,
		Timestamp:  ev.Timestamp,
	}

	if !ev.Partial {
		// Full fill is terminal — remove routing info before dispatching.
		r.RemoveOrderTracking(routeKey)
		if routeKey != ev.OrderID {
			r.RemoveOrderTracking(ev.OrderID) // drop the exchange-id alias too
		}

		if botID != "" {
			r.mu.RLock()
			hand, ok := r.hands[botID]
			r.mu.RUnlock()
			if ok {
				// Route to hand: hand.applyFill owns exit-level management, poslog,
				// and metrics; it calls rt.ReportFill itself. EnqueueFill never drops
				// (unbounded mailbox), so the hand always processes its own fill.
				// Publish trade.filled here (before the hand processes asynchronously).
				hand.EnqueueFill(ev)
				if r.js != nil {
					natsapi.PublishTradeFill(r.js, r.tradeFillMsg(botID, ev))
				}
				r.MarkOrderFillPublished(ev.OrderID)
				return
			}
		}
		// Orphan path (no owning hand): apply fill directly to portfolio.
		r.ReportFill(fillReport)
	} else {
		// Partial fill: order still open — keep routing info for subsequent fills.
		// Apply incremental qty to portfolio so P&L is current.
		// Record applied qty so REST poll path can subtract to avoid double-counting.
		if botID != "" {
			r.mu.RLock()
			hand, ok := r.hands[botID]
			r.mu.RUnlock()
			if ok {
				hand.MarkPartialApplied(ev.OrderID, ev.FilledQty)
			}
		}
		r.ReportFill(fillReport)
	}

	// Orphan and partial paths: emit fill event directly.
	// (Normal hand path emits its own CodeOrderFilled via hand.applyFill → EmitEvent.)
	r.EmitEvent(natsapi.HelmEvent{
		HandID:  botID,
		Code:    CodeOrderFilled,
		Symbol:  ev.Symbol,
		Side:    string(ev.Side),
		Qty:     ev.FilledQty,
		Price:   ev.FilledAvg,
		OrderID: ev.OrderID,
		Msg:     "runtime: fill applied to portfolio",
	})

	if r.js != nil {
		natsapi.PublishTradeFill(r.js, r.tradeFillMsg(botID, ev))
		if !ev.Partial {
			r.MarkOrderFillPublished(ev.OrderID)
		}
	}
}

// tradeFillMsg builds the TransactionMsg for natsapi.PublishTradeFill.
func (r *HelmRuntime) tradeFillMsg(botID string, ev exchange.WsFillEvent) natsapi.TransactionMsg {
	return natsapi.TransactionMsg{
		HelmID:    r.HelmID.String(),
		AccountID: r.AccountID.String(),
		UserID:    r.UserID.String(),
		HandID:    botID,
		TradeID:   ev.TradeID,
		OrderID:   ev.OrderID,
		Kind:      "fill",
		Symbol:    ev.Symbol,
		Side:      string(ev.Side),
		Qty:       ev.FilledQty,
		AvgPrice:  ev.FilledAvg,
		FilledAt:  ev.Timestamp,
	}
}
