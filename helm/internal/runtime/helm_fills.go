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
	"mallow/helm/internal/safe"
)

// StartStreaming opens the WS order stream and starts the fill and lifecycle
// drain goroutines. No-op if the exchange does not implement AccountStreamer.
// appCtx governs drain goroutine lifetime; a separate per-stream context controls
// the WS connection so it can be replaced on RotateStream without stopping drains.
func (r *HelmRuntime) StartStreaming(appCtx context.Context) {
	drainCtx, drainCancel := context.WithCancel(appCtx)
	streamCtx, streamCancel := context.WithCancel(drainCtx)

	r.fillStreamMu.Lock()
	r.fillStreamCancel = streamCancel
	r.fillDrainCancel = drainCancel
	r.fillStreamMu.Unlock()

	if !r.connectStream(streamCtx) {
		streamCancel()
		drainCancel()
		r.fillStreamMu.Lock()
		r.fillStreamCancel = nil
		r.fillDrainCancel = nil
		r.fillStreamMu.Unlock()
		return
	}
	go r.runLifecycleProcessor(drainCtx)
	go r.runFillProcessor(drainCtx)
}

// RotateStream replaces credentials and reconnects the WS stream.
// Drain goroutines (runLifecycleProcessor, runFillProcessor) keep running on appCtx —
// only the WS connection itself is torn down and re-opened.
func (r *HelmRuntime) RotateStream(appCtx context.Context, newCreds exchange.Credentials) {
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
// Does NOT start drain goroutines — that is StartStreaming's job.
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
		r.handlePositionEvent,
		r.handleRiskEvent,
		r.TriggerAuthError,
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

// handlePositionEvent updates the helm's knowledge of its current futures position.
// Currently, logs the event; future: update a per-symbol position cache for risk checks.
func (r *HelmRuntime) handlePositionEvent(ev exchange.PositionEvent) {
	slog.Info("position update",
		"helm_id", r.HelmID,
		"symbol", ev.Symbol,
		"side", ev.Side,
		"size", ev.Size,
		"entry_px", ev.EntryPrice,
		"upnl", ev.UnrealizedPnL,
	)
}

// handleRiskEvent handles margin call / liquidation warnings.
// Currently logs; future: trigger helm pause or notify via NATS helm.events.*.
func (r *HelmRuntime) handleRiskEvent(ev exchange.RiskEvent) {
	slog.Warn("risk event",
		"helm_id", r.HelmID,
		"symbol", ev.Symbol,
		"margin_ratio", ev.MarginRatio,
		"liq_price", ev.LiquidationPrice,
	)
}

// runLifecycleProcessor drains lifecycleQueue in batch on each lifecycleSignal wakeup.
//   - Live:     informational — our orders are already tracked by clid before placement
//   - Canceled: remove from orderbook and notify the owning hand
func (r *HelmRuntime) runLifecycleProcessor(ctx context.Context) {
	defer safe.Recover()
	for {
		select {
		case <-r.lifecycleSignal:
			r.lifecycleMu.Lock()
			batch := r.lifecycleQueue
			r.lifecycleQueue = nil
			r.lifecycleMu.Unlock()
			for _, ev := range batch {
				r.applyLifecycleEvent(ctx, ev)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (r *HelmRuntime) applyLifecycleEvent(ctx context.Context, ev exchange.OrderLifecycleEvent) {
	switch ev.Type {
	case exchange.OrderLifecycleEventLive:
		slog.Debug("order: live ack",
			"helm_id", r.HelmID,
			"order_id", ev.OrderID,
			"client_order_id", ev.ClientOrderID,
			"symbol", ev.Symbol,
		)
	case exchange.OrderLifecycleEventCanceled:
		key := clid.CanonKey(ev.ClientOrderID, ev.OrderID)
		handID := r.PendingOrderHandID(key)
		r.RemoveOrderTracking(key)
		if key != ev.OrderID {
			r.RemoveOrderTracking(ev.OrderID)
		}
		slog.Info("order tracking: canceled order removed",
			"helm_id", r.HelmID,
			"order_id", ev.OrderID,
		)
		if handID != "" {
			r.mu.RLock()
			entry, ok := r.hands[handID]
			r.mu.RUnlock()
			if ok {
				// Route through the hand's actor loop with a 1s head-start
				// for the paired OCO fill to arrive first.
				//
				// Binance OCO behavior: when the SL leg fills, the exchange
				// auto-cancels the TP leg and sends both events on the same WS
				// connection. In practice the cancel arrives ~2ms before the
				// fill. Without the delay, HandleExitOrderCanceled would see the
				// TP cancel, find no entry in pendingCancels, and incorrectly
				// disown the position before the SL fill is even processed.
				//
				// 1s gives the fill event more than enough time to travel
				// through helm_fills → EnqueueFill → drainFills → applyFill →
				// cancelExitOrders, which populates pendingCancels with the TP
				// order ID. When the delayed cancel finally arrives via
				// exitCancelCh, HandleExitOrderCanceled finds it in pendingCancels
				// and returns early — no false orphan.
				//
				// 2026-07-10: this predates HandleExitOrderCanceled's Case 3
				// PhaseOpen branch (remainingCount > 0, added 2026-06-30 —
				// this delay is from 2026-06-07). Traced through today: with that
				// branch in place, the race this delay guards against should now
				// self-resolve without it — the cancelled ID's sibling is still in
				// ExchangeOrderIDs at the moment the cancel is processed (the fill
				// hasn't removed it yet), so remainingCount > 0 and Case 3 already
				// returns early on its own. Can probably be removed now — leaving it
				// as-is (belt-and-suspenders, not costing anything) rather than
				// pulling a safety net that's been live this long without re-verifying
				// against production traffic first.
				handCtx := ctx
				go func(orderID string) {
					defer safe.Recover()
					t := time.NewTimer(1 * time.Second)
					defer t.Stop()
					select {
					case <-t.C:
					case <-handCtx.Done():
						return
					}
					select {
					case entry.h.exitCancelCh <- orderID:
					case <-handCtx.Done():
					}
				}(ev.OrderID)
			}
		}
	}
}

// runFillProcessor drains wsFillQueue in batch on each wsFillSignal wakeup.
// Batching amortizes the per-event overhead when fill bursts arrive (e.g.
// multiple partial fills from OKX OCO execution).
func (r *HelmRuntime) runFillProcessor(ctx context.Context) {
	defer safe.Recover()
	for {
		select {
		case <-r.wsFillSignal:
			r.wsFillMu.Lock()
			batch := r.wsFillQueue
			r.wsFillQueue = nil
			r.wsFillMu.Unlock()
			for _, ev := range batch {
				r.applyWsFill(ctx, ev)
			}
		case <-ctx.Done():
			return
		}
	}
}

// applyWsFill processes a single WS fill event.
// Fulfills are routed to the owning hand (poslog + metrics + exit logic).
// Partial fills are applied incrementally to the portfolio and tracked for REST dedup.
func (r *HelmRuntime) applyWsFill(ctx context.Context, ev exchange.WsFillEvent) {
	// Normalize commission to quote currency and adjust qty for buys where fee is paid in the base asset or standard non-quote assets like BNB.
	ev.FilledQty, ev.Commission = r.normalizeCommission(ctx, ev.Symbol, ev.Side, ev.FilledQty, ev.FilledAvg, ev.Commission, ev.CommissionAsset)

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
		// Fulfill is terminal — remove routing info before dispatching.
		r.RemoveOrderTracking(routeKey)
		if routeKey != ev.OrderID {
			r.RemoveOrderTracking(ev.OrderID) // drop the exchange-id alias too
		}

		if botID != "" {
			r.mu.RLock()
			entry, ok := r.hands[botID]
			r.mu.RUnlock()
			if ok {
				// Route to hand: hand.applyFill owns exit-level management, poslog,
				// and metrics; it calls rt.ReportFill itself. EnqueueFill never drops
				// (unbounded mailbox), so the hand always processes its own fill.
				// Publish trade.filled here (before the hand processes asynchronously).
				entry.h.EnqueueFill(ev)
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
			entry, ok := r.hands[botID]
			r.mu.RUnlock()
			if ok {
				entry.h.MarkPartialApplied(ev.OrderID, ev.FilledQty, ev.FilledAvg, ev.Commission)
				// Pre-mark the sibling bracket order as a pending-cancel so
				// HandleExitOrderCanceled treats the OCO auto-cancel as helm-initiated
				// even if it arrives before the final partial fill is fully applied.
				// Fixes the race: Binance fires SL-cancel ~1ms after TP partial fill;
				// with only MarkPartialApplied run (no cancelExitOrders), pendingCancels
				// is empty → cancel misidentified as external close → spurious EXT_CLOSE.
				entry.h.NotifyBracketPartialFill(ev.OrderID)
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

	// Only publish trade.filled for the final fill, not for each partial.
	// Each partial already records incremental qty via MarkPartialApplied;
	// downstream consumers (PnL reporting, trade log) expect one event per order.
	if r.js != nil && !ev.Partial {
		natsapi.PublishTradeFill(r.js, r.tradeFillMsg(botID, ev))
		r.MarkOrderFillPublished(ev.OrderID)
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
		Fee:       ev.Commission,
		FilledAt:  ev.Timestamp,
	}
}
