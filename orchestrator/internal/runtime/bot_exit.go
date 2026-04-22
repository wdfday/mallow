package runtime

import (
	"context"
	"log/slog"
	"time"

	"orchestrator/internal/infra/exchange"
)

// flattenPositions closes all open positions with market orders. Called by Kill.
func (b *Bot) flattenPositions(ctx context.Context) {
	positions := b.rt.Portfolio.Positions()
	for _, pos := range positions {
		side := exchange.Sell
		if pos.Qty.IsNegative() {
			side = exchange.Buy
		}
		qty := pos.Qty.Abs()
		result, err := b.rt.Exchange.PlaceOrder(ctx, b.rt.Creds, exchange.OrderRequest{
			Symbol: pos.Symbol,
			Side:   side,
			Type:   exchange.Market,
			Qty:    qty,
		})
		if err != nil {
			slog.Error("bot: kill flatten failed", "bot_id", b.id, "symbol", pos.Symbol, "err", err)
			continue
		}
		slog.Info("bot: kill flatten order placed", "bot_id", b.id, "symbol", pos.Symbol, "side", side, "qty", qty, "order_id", result.ID)
		b.metrics.ordersPlaced.Add(1)
	}
}

// checkExits scans open positions against locally stored SL/TP levels.
// Called on every pollTicker tick (every 5s) as a safety net in case
// exchange-side bracket orders fail to execute.
func (b *Bot) checkExits() {
	b.mu.RLock()
	exits := make(map[string]exitLevel, len(b.exitLevels))
	for sym, el := range b.exitLevels {
		exits[sym] = el
	}
	b.mu.RUnlock()

	for sym, el := range exits {
		price := b.rt.lastKnownPrice(sym)
		if !price.IsPositive() {
			continue
		}
		var reason string
		if el.Side == "buy" { // long position
			if el.StopLoss.IsPositive() && price.LessThanOrEqual(el.StopLoss) {
				reason = "stop_loss"
			} else if el.TakeProfit.IsPositive() && price.GreaterThanOrEqual(el.TakeProfit) {
				reason = "take_profit"
			}
		} else { // short position
			if el.StopLoss.IsPositive() && price.GreaterThanOrEqual(el.StopLoss) {
				reason = "stop_loss"
			} else if el.TakeProfit.IsPositive() && price.LessThanOrEqual(el.TakeProfit) {
				reason = "take_profit"
			}
		}
		if reason == "" {
			continue
		}
		slog.Info("exit monitor triggered", "bot_id", b.id, "symbol", sym,
			"reason", reason, "price", price,
			"stop_loss", el.StopLoss, "take_profit", el.TakeProfit)
		b.mu.Lock()
		delete(b.exitLevels, sym)
		b.mu.Unlock()
		select {
		case b.UrgentSignals <- Signal{Symbol: sym, Direction: "close", Strength: 1.0, ReceivedAt: time.Now()}:
		default:
			slog.Warn("exit monitor: urgent channel full", "bot_id", b.id, "symbol", sym)
		}
	}
}

// OnL2 is called by Orchestrator.UpdateL2 on every books5 snapshot for this
// bot's symbol. It runs in the shared OKX market streamer goroutine and must
// return quickly. The first 10 snapshots (~1s at 100ms cadence) are skipped
// while the guard warms up.
//
// Exit rule evaluation is intentionally deferred — plug in an L2ExitRule here.
func (b *Bot) OnL2(snap exchange.L2Snapshot) {
	if b.IsPaused() {
		return
	}
	b.mu.Lock()
	g, ok := b.l2Guards[snap.Symbol]
	if !ok {
		g = &l2Guard{warmupLeft: 10}
		b.l2Guards[snap.Symbol] = g
	}
	if g.warmupLeft > 0 {
		g.warmupLeft--
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	// TODO: evaluate pluggable L2ExitRule(snap) → b.UrgentSignals
}
