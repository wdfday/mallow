package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"orchestrator/internal/infra/exchange"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
	"orchestrator/internal/module/worker/domain"
	"orchestrator/internal/runtime/core/strategy"
	"orchestrator/internal/runtime/core/tactics"
	"orchestrator/internal/runtime/orderbook"
)

// Bot is an autonomous trading agent.
// Each Bot owns its own strategy, tactician, signal channel, and run-loop goroutine.
// Account-level resources (Exchange, Portfolio, OrderBook) are shared via OrchestratorRuntime.
type Bot struct {
	id             string
	orchestratorID string
	rt             *OrchestratorRuntime
	strategy       strategy.Strategy
	tactician      *tactics.Tactician
	limiter        *rate.Limiter

	// Signals receives regular (non-urgent) entry/exit signals.
	// Buffer=1 with drain-replace: always holds the latest signal, never stale ones.
	Signals chan Signal

	// UrgentSignals receives close/exit signals that must not be dropped.
	// Buffer=4 to absorb bursts without blocking the dispatcher.
	// Drained with priority in the run-loop before Signals.
	UrgentSignals chan Signal

	// Metadata — set by the service layer, used for runtime bookkeeping and display.
	Symbol       string
	StrategyName string
	CapitalPct   float64

	// WasRunning remembers pre-pause state so OrchestratorRuntime.Resume can restore the bot.
	WasRunning bool

	mu             sync.RWMutex
	running        bool
	paused         bool
	cancel         context.CancelFunc
	done           chan struct{}
	orders         []domain.Order
	barsSinceEntry map[string]int // symbol → bar count since position entry (time-stop)

	health  BotHealth
	metrics struct {
		signalsReceived atomic.Int64
		signalsFiltered atomic.Int64
		signalsDropped  atomic.Int64 // channel-full drops (non-urgent only)
		tradesApproved  atomic.Int64
		ordersPlaced    atomic.Int64
		ordersFilled    atomic.Int64
		ordersFailed    atomic.Int64
		mu              sync.Mutex
		totalPnL        float64
		winCount        int64
		lossCount       int64
	}
}

// ID returns the bot's unique identifier.
func (b *Bot) ID() string { return b.id }

func timePtr(t time.Time) *time.Time { return &t }

// RecordDrop increments the dropped-signal counter. Called by the dispatcher.
func (b *Bot) RecordDrop() { b.metrics.signalsDropped.Add(1) }

// DeliverSignal enqueues a signal onto the appropriate bot channel.
// Urgent signals are prioritized and never silently replaced; regular signals
// use drain-replace semantics so only the freshest entry signal is kept.
func (b *Bot) DeliverSignal(sig Signal) {
	if sig.IsUrgent() {
		select {
		case b.UrgentSignals <- sig:
		default:
			slog.Error("bot urgent signal channel full, dropping close signal",
				"bot_id", b.id, "symbol", sig.Symbol)
		}
		return
	}

	select {
	case <-b.Signals:
	default:
	}
	select {
	case b.Signals <- sig:
	default:
		b.RecordDrop()
		slog.Warn("bot signal channel full after drain, dropping",
			"bot_id", b.id, "symbol", sig.Symbol)
	}
}

// NewBot creates a Bot. Call Start() to spawn its run-loop goroutine.
func NewBot(
	id, orchestratorID string,
	rt *OrchestratorRuntime,
	strat strategy.Strategy,
	tact *tactics.Tactician,
) *Bot {
	return &Bot{
		id:             id,
		orchestratorID: orchestratorID,
		rt:             rt,
		strategy:       strat,
		tactician:      tact,
		limiter:        rate.NewLimiter(rate.Every(1*time.Second), 5),
		Signals:        make(chan Signal, 1),
		UrgentSignals:  make(chan Signal, 4),
		orders:         make([]domain.Order, 0, 256),
		barsSinceEntry: make(map[string]int),
		health:         BotHealth{Status: "stopped"},
	}
}

// BuildBotComponents translates a BotConfig into a Strategy + Tactician.
// Called by the service layer when creating or updating a bot.
func BuildBotComponents(cfg domain.BotConfig) (strategy.Strategy, *tactics.Tactician) {
	minStrength := cfg.Tactic.MinStrength
	if minStrength <= 0 {
		minStrength = 0.3
	}

	var strat strategy.Strategy
	switch cfg.Tactic.StrategyName() {
	default:
		strat = strategy.NewSignalFollower(minStrength)
	}

	sizingMode := tactics.SizingFixedFractional
	switch cfg.Risk.SizeMode {
	case "fixed_qty":
		sizingMode = tactics.SizingFixedQty
	case "volatility":
		sizingMode = tactics.SizingVolatility
	case "percent_equity":
		sizingMode = tactics.SizingPercentEquity
	}

	tact := tactics.New(tactics.SizingConfig{
		Mode:              sizingMode,
		RiskPerTradePct:   cfg.Risk.RiskPerTradePct,
		MaxPositionPct:    cfg.Risk.MaxPositionPct,
		FixedQty:          cfg.Risk.FixedQty,
		StopLossATRMult:   cfg.Risk.StopLossATRMult,
		TakeProfitATRMult: cfg.Risk.TakeProfitATRMult,
		StopLossPct:       cfg.Risk.StopLossPct,
		TakeProfitPct:     cfg.Risk.TakeProfitPct,
		TrailingStopPct:   cfg.Risk.TrailingStopPct,
		MaxBarsHeld:       cfg.Risk.MaxBarsHeld,
	})

	return strat, tact
}

// Start spawns the bot's run-loop goroutine.
func (b *Bot) Start() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return
	}
	b.running = true
	b.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.health.Status = "running"
	b.health.StartedAt = timePtr(time.Now().UTC())
	go b.run(ctx)
	slog.Info("bot started", "bot_id", b.id, "exchange", b.rt.Exchange.Name())
}

// Stop cancels the run-loop and waits for it to exit.
func (b *Bot) Stop() {
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return
	}
	b.running = false
	b.health.Status = "stopped"
	cancel := b.cancel
	done := b.done
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	slog.Info("bot stopped", "bot_id", b.id)
}

func (b *Bot) IsRunning() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.running
}

// IsPaused reports whether the bot is individually paused (ignores runtime-level pause).
func (b *Bot) IsPaused() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.paused
}

// Pause suspends signal processing for this bot without stopping its goroutine.
// The bot continues to poll pending orders and will still respond to urgent signals.
func (b *Bot) Pause() {
	b.mu.Lock()
	b.paused = true
	b.health.Status = "paused"
	b.mu.Unlock()
	slog.Info("bot paused", "bot_id", b.id)
}

// Resume re-enables signal processing after a Pause.
func (b *Bot) Resume() {
	b.mu.Lock()
	if b.running {
		b.paused = false
		b.health.Status = "running"
	}
	b.mu.Unlock()
	slog.Info("bot resumed", "bot_id", b.id)
}

// Kill stops the bot and immediately closes all open positions via market orders.
// This is a best-effort emergency flatten — errors are logged but do not abort.
func (b *Bot) Kill(ctx context.Context) {
	slog.Warn("bot: kill initiated — flattening all positions", "bot_id", b.id)

	// Pause first so no new signals are processed while we flatten.
	b.mu.Lock()
	b.paused = true
	b.health.Status = "killed"
	b.mu.Unlock()

	b.flattenPositions(ctx)
	b.Stop()
}

// flattenPositions closes every open position held by this bot's orchestrator portfolio.
func (b *Bot) flattenPositions(ctx context.Context) {
	positions := b.rt.Portfolio.Positions()
	for _, pos := range positions {
		side := exchange.Sell
		if pos.Qty < 0 {
			side = exchange.Buy
		}
		qty := pos.Qty
		if qty < 0 {
			qty = -qty
		}
		result, err := b.rt.Exchange.PlaceOrder(ctx, exchange.OrderRequest{
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

func (b *Bot) Orders() []domain.Order {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]domain.Order, len(b.orders))
	copy(result, b.orders)
	return result
}

// Health returns a snapshot of the bot's health state.
func (b *Bot) Health() BotHealth {
	b.mu.RLock()
	defer b.mu.RUnlock()
	h := b.health
	if b.running && b.health.StartedAt != nil {
		h.Uptime = time.Since(*b.health.StartedAt).Truncate(time.Second).String()
	}
	return h
}

// Metrics returns a snapshot of the bot's trading metrics.
func (b *Bot) Metrics() BotMetrics {
	b.metrics.mu.Lock()
	defer b.metrics.mu.Unlock()
	return BotMetrics{
		SignalsReceived: b.metrics.signalsReceived.Load(),
		SignalsFiltered: b.metrics.signalsFiltered.Load(),
		SignalsDropped:  b.metrics.signalsDropped.Load(),
		TradesApproved:  b.metrics.tradesApproved.Load(),
		OrdersPlaced:    b.metrics.ordersPlaced.Load(),
		OrdersFilled:    b.metrics.ordersFilled.Load(),
		OrdersFailed:    b.metrics.ordersFailed.Load(),
		TotalPnL:        b.metrics.totalPnL,
		WinCount:        b.metrics.winCount,
		LossCount:       b.metrics.lossCount,
	}
}

func (b *Bot) run(ctx context.Context) {
	defer close(b.done)
	pollTicker := time.NewTicker(5 * time.Second)
	defer pollTicker.Stop()
	staleTicker := time.NewTicker(30 * time.Second)
	defer staleTicker.Stop()

	for {
		// Priority: drain all urgent signals before processing regular ones.
		select {
		case sig := <-b.UrgentSignals:
			b.handleSignal(ctx, sig)
			continue
		default:
		}

		select {
		case sig := <-b.UrgentSignals:
			b.handleSignal(ctx, sig)
		case sig := <-b.Signals:
			b.handleSignal(ctx, sig)
		case <-pollTicker.C:
			b.pollOrders(ctx)
		case <-staleTicker.C:
			b.checkStale()
		case <-ctx.Done():
			return
		}
	}
}

func (b *Bot) checkStale() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.running || b.health.LastSignalAt == nil {
		return
	}
	if time.Since(*b.health.LastSignalAt) > 5*time.Minute {
		b.health.Status = "stale"
	}
}

func (b *Bot) handleSignal(ctx context.Context, sig Signal) {
	b.metrics.signalsReceived.Add(1)

	// Discard stale non-urgent signals — stale close signals are always processed.
	if !sig.IsUrgent() && !sig.ReceivedAt.IsZero() && time.Since(sig.ReceivedAt) > signalMaxAge {
		b.metrics.signalsFiltered.Add(1)
		slog.Debug("bot: stale signal discarded", "bot_id", b.id, "symbol", sig.Symbol, "age", time.Since(sig.ReceivedAt).Truncate(time.Millisecond))
		return
	}

	b.mu.Lock()
	b.health.LastSignalAt = timePtr(time.Now().UTC())
	if b.health.Status == "stale" {
		b.health.Status = "running"
	}
	b.mu.Unlock()

	if b.rt.IsPaused() || b.IsPaused() {
		b.metrics.signalsFiltered.Add(1)
		return
	}
	if !b.limiter.Allow() {
		b.metrics.signalsFiltered.Add(1)
		slog.Warn("bot: signal rate limited", "bot_id", b.id, "symbol", sig.Symbol)
		return
	}

	signal := strategy.Signal{
		Symbol:    sig.Symbol,
		Direction: sig.Direction,
		Strength:  sig.Strength,
		Timestamp: time.Now().UTC(),
	}
	intent := b.strategy.Evaluate(signal)

	if sig.Direction == "close" {
		if pos := b.rt.Portfolio.GetPosition(sig.Symbol); pos != nil && pos.Qty < 0 {
			intent.Action = strategy.ActionExitShort
		}
	}

	// Time-stop: if MaxBarsHeld is configured, count each incoming signal as one bar.
	// When the counter hits the limit, force-close the position.
	if maxBars := b.tactician.MaxBarsHeld(); maxBars > 0 {
		if pos := b.rt.Portfolio.GetPosition(sig.Symbol); pos != nil && pos.Qty != 0 {
			b.mu.Lock()
			b.barsSinceEntry[sig.Symbol]++
			bars := b.barsSinceEntry[sig.Symbol]
			b.mu.Unlock()
			if bars >= maxBars {
				if pos.Qty > 0 {
					intent.Action = strategy.ActionExitLong
				} else {
					intent.Action = strategy.ActionExitShort
				}
				slog.Info("bot: time-stop triggered", "bot_id", b.id, "symbol", sig.Symbol, "bars", bars)
			}
		}
	}

	reply := b.rt.ProcessTrade(ctx, TradeProposal{
		BotID:  b.id,
		Symbol: sig.Symbol,
		Intent: intent,
		ATR:    sig.ATR,
	}, b.tactician)

	if !reply.Approved {
		b.metrics.signalsFiltered.Add(1)
		slog.Debug("bot: trade rejected", "bot_id", b.id, "symbol", sig.Symbol, "reason", reply.Reason)
		return
	}
	b.metrics.tradesApproved.Add(1)

	orderType := exchange.Market
	limitPrice := 0.0
	if reply.EntryType == "limit" && reply.LimitPrice > 0 {
		orderType = exchange.Limit
		limitPrice = reply.LimitPrice
	}

	result, err := b.rt.Exchange.PlaceOrder(ctx, exchange.OrderRequest{
		Symbol:       sig.Symbol,
		Side:         exchange.OrderSide(reply.Side),
		Type:         orderType,
		Qty:          reply.Qty,
		Price:        limitPrice,
		StopLoss:     reply.StopLoss,
		TakeProfit:   reply.TakeProfit,
		TrailingStop: reply.TrailingStop,
	})
	if err != nil {
		b.metrics.ordersFailed.Add(1)
		b.mu.Lock()
		b.health.LastErrorAt = timePtr(time.Now().UTC())
		b.health.LastError = err.Error()
		b.health.Status = "error"
		b.mu.Unlock()
		slog.Error("bot: order failed", "bot_id", b.id, "symbol", sig.Symbol, "err", err)
		return
	}
	b.metrics.ordersPlaced.Add(1)

	b.rt.TrackOrder(orderbook.PendingOrder{
		OrderID:   result.ID,
		BotID:     b.id,
		AccountID: b.orchestratorID,
		Symbol:    sig.Symbol,
		Side:      orderbook.OrderSide(reply.Side),
		Qty:       reply.Qty,
	})

	now := time.Now().UTC()
	order := domain.Order{
		BotID:          b.id,
		OrchestratorID: b.orchestratorID,
		ID:             result.ID,
		Symbol:         sig.Symbol,
		Side:           reply.Side,
		Qty:            reply.Qty,
		Type:           "market",
		Status:         result.Status,
		FilledQty:      result.FilledQty,
		FilledAvg:      result.FilledAvg,
		SubmitTime:     now,
	}
	b.mu.Lock()
	b.orders = append(b.orders, order)
	b.health.LastOrderAt = timePtr(now)
	if b.health.Status == "error" {
		b.health.Status = "running"
	}
	b.mu.Unlock()

	slog.Info("bot: order placed", "bot_id", b.id, "order_id", order.ID,
		"symbol", sig.Symbol, "side", reply.Side, "qty", reply.Qty)

	if result.Status == "filled" {
		b.applyFill(result.ID, sig.Symbol, reply.Side, result.FilledQty, result.FilledAvg)
	}
}

func (b *Bot) applyFill(orderID, symbol, side string, qty, price float64) {
	b.metrics.ordersFilled.Add(1)

	// Calculate PnL when closing a long (sell) or covering a short (buy).
	// Snapshot entry price before ReportFill updates the portfolio position.
	if price > 0 {
		if pos := b.rt.Portfolio.GetPosition(symbol); pos != nil && pos.Qty != 0 {
			var pnl float64
			if side == "sell" && pos.Qty > 0 {
				pnl = (price - pos.AvgPrice) * qty
				// Closing a long — reset time-stop counter.
				b.mu.Lock()
				delete(b.barsSinceEntry, symbol)
				b.mu.Unlock()
			} else if side == "buy" && pos.Qty < 0 {
				pnl = (pos.AvgPrice - price) * qty
				// Covering a short — reset time-stop counter.
				b.mu.Lock()
				delete(b.barsSinceEntry, symbol)
				b.mu.Unlock()
			}
			if pnl != 0 {
				b.metrics.mu.Lock()
				b.metrics.totalPnL += pnl
				if pnl > 0 {
					b.metrics.winCount++
				} else {
					b.metrics.lossCount++
				}
				b.metrics.mu.Unlock()
			}
		}
	}

	b.rt.ReportFill(orchdomain.FillReport{
		BotID:     b.id,
		AccountID: b.orchestratorID,
		OrderID:   orderID,
		Symbol:    symbol,
		Side:      side,
		Qty:       qty,
		Price:     price,
		Timestamp: time.Now().UTC(),
	})
}

func (b *Bot) pollOrders(ctx context.Context) {
	b.mu.RLock()
	var pending []domain.Order
	for _, o := range b.orders {
		switch o.Status {
		case "new", "accepted", "pending_new", "partially_filled", "submitted":
			pending = append(pending, o)
		}
	}
	b.mu.RUnlock()

	for _, o := range pending {
		result, err := b.rt.Exchange.GetOrder(ctx, o.ID)
		if err != nil {
			slog.Warn("bot: poll order failed", "order_id", o.ID, "err", err)
			continue
		}
		b.mu.Lock()
		for i := range b.orders {
			if b.orders[i].ID == o.ID {
				b.orders[i].Status = result.Status
				b.orders[i].FilledQty = result.FilledQty
				b.orders[i].FilledAvg = result.FilledAvg
				break
			}
		}
		b.mu.Unlock()
		if result.Status == "filled" {
			side := "buy"
			if result.Side == exchange.Sell {
				side = "sell"
			}
			b.applyFill(result.ID, result.Symbol, side, result.FilledQty, result.FilledAvg)
		}
	}
}

// PlaceOrder submits an order via the full pipeline. Manual/legacy interface.
func (b *Bot) PlaceOrder(ctx context.Context, symbol string, qty float64, side string) (string, error) {
	if !b.IsRunning() {
		return "", fmt.Errorf("bot is not running")
	}
	if qty <= 0 {
		return "", fmt.Errorf("invalid quantity: %f", qty)
	}
	direction := "long"
	if side == "sell" {
		direction = "close"
	}
	signal := strategy.Signal{
		Symbol:    symbol,
		Direction: direction,
		Strength:  1.0,
		Timestamp: time.Now().UTC(),
	}
	intent := b.strategy.Evaluate(signal)
	reply := b.rt.ProcessTrade(ctx, TradeProposal{
		BotID:  b.id,
		Symbol: symbol,
		Intent: intent,
	}, b.tactician)
	if !reply.Approved {
		return "", fmt.Errorf("trade rejected: %s", reply.Reason)
	}
	result, err := b.rt.Exchange.PlaceOrder(ctx, exchange.OrderRequest{
		Symbol: symbol,
		Side:   exchange.OrderSide(side),
		Type:   exchange.Market,
		Qty:    reply.Qty,
	})
	if err != nil {
		b.metrics.ordersFailed.Add(1)
		return "", fmt.Errorf("place order: %w", err)
	}
	b.metrics.ordersPlaced.Add(1)
	b.rt.TrackOrder(orderbook.PendingOrder{
		OrderID:   result.ID,
		BotID:     b.id,
		AccountID: b.orchestratorID,
		Symbol:    symbol,
		Side:      orderbook.OrderSide(side),
		Qty:       reply.Qty,
	})
	order := domain.Order{
		BotID:          b.id,
		OrchestratorID: b.orchestratorID,
		ID:             result.ID,
		Symbol:         symbol,
		Side:           side,
		Qty:            reply.Qty,
		Type:           "market",
		Status:         result.Status,
		FilledQty:      result.FilledQty,
		FilledAvg:      result.FilledAvg,
		SubmitTime:     time.Now().UTC(),
	}
	b.mu.Lock()
	b.orders = append(b.orders, order)
	b.health.LastOrderAt = timePtr(time.Now().UTC())
	b.mu.Unlock()
	if result.Status == "filled" {
		b.applyFill(result.ID, symbol, side, result.FilledQty, result.FilledAvg)
	}
	return order.ID, nil
}
