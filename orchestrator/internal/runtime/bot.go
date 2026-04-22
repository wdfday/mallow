package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"
	"golang.org/x/time/rate"

	"orchestrator/internal/infra/exchange"
	"orchestrator/internal/module/bot/domain"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
	"orchestrator/internal/runtime/core/strategy"
	"orchestrator/internal/runtime/core/tactics"
	"orchestrator/internal/runtime/orderbook"
)

// exitLevel tracks the local SL/TP levels for an open position.
// Populated when an entry order fills; cleared when the position is closed.
type exitLevel struct {
	Side       string          // opening side: "buy" (long) or "sell" (short)
	StopLoss   decimal.Decimal // zero = not set
	TakeProfit decimal.Decimal // zero = not set
}

// l2Guard holds per-symbol warm-up state for reactive L2 monitoring.
// Prevents reactions on the first few snapshots when the bot just started.
type l2Guard struct {
	warmupLeft int // countdown; bot reacts only after this reaches zero
}

// Bot is an autonomous trading agent.
// Each Bot owns its own strategy, tactician, signal channel, and run-loop goroutine.
// Account-level resources (Exchange, Portfolio, OrderBook) are shared via OrchestratorRuntime.
type Bot struct {
	id             string
	orchestratorID string
	rt             *Orchestrator
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
	barsSinceEntry map[string]int       // symbol → bar count since position entry (time-stop)
	pendingExits   map[string]exitLevel // orderID → SL/TP levels; promoted to exitLevels on entry fill
	exitLevels     map[string]exitLevel // symbol → active SL/TP for open position (local safety net)
	l2Guards       map[string]*l2Guard  // symbol → L2 warm-up state

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
		totalPnL        decimal.Decimal
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
	rt *Orchestrator,
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
		pendingExits:   make(map[string]exitLevel),
		exitLevels:     make(map[string]exitLevel),
		l2Guards:       make(map[string]*l2Guard),
		health:         BotHealth{Status: "stopped"},
	}
}

// BuildBotComponents translates a BotInstance into a Strategy + Tactician.
func BuildBotComponents(b *domain.BotInstance) (strategy.Strategy, *tactics.Tactician) {
	minStrength := b.Strategy.MinStrength
	if minStrength <= 0 {
		minStrength = 0.3
	}

	var strat strategy.Strategy
	switch b.Strategy.Key() {
	default:
		strat = strategy.NewSignalFollower(minStrength)
	}

	sizingMode := tactics.SizingFixedFractional
	switch b.Position.SizeMode {
	case "fixed_qty":
		sizingMode = tactics.SizingFixedQty
	case "volatility":
		sizingMode = tactics.SizingVolatility
	case "percent_equity":
		sizingMode = tactics.SizingPercentEquity
	}

	sc := tactics.SizingConfig{
		Mode:             sizingMode,
		AllocatedCapital: b.Position.AllocatedCapital,
		AllocatedPct:     b.Position.AllocatedPct,
		UnitCapital:      b.Position.UnitCapital,
		UnitPct:          b.Position.UnitPct,
		MaxPositions:     b.Position.MaxPositions,
		RiskPerTradePct:  b.Position.RiskPerTradePct,
		MaxPositionPct:   b.Position.MaxPositionPct,
		FixedQty:         b.Position.FixedQty,
		TrailingStopPct:  b.Risk.TrailingStopPct,
	}
	if e := b.Risk.Exit; e != nil {
		if e.SL != nil {
			sc.StopLossATRMult, sc.StopLossPct = e.SL.ATRMult(), e.SL.PctValue()
		}
		if e.TP != nil {
			sc.TakeProfitATRMult, sc.TakeProfitPct = e.TP.ATRMult(), e.TP.PctValue()
		}
		if e.MaxBars != nil {
			sc.MaxBarsHeld = *e.MaxBars
		}
	}
	tact := tactics.New(sc)

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

// IsPaused reports whether the bot is individually paused.
func (b *Bot) IsPaused() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.paused
}

// Pause suspends signal processing for this bot without stopping its goroutine.
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
func (b *Bot) Kill(ctx context.Context) {
	slog.Warn("bot: kill initiated — flattening all positions", "bot_id", b.id)
	b.mu.Lock()
	b.paused = true
	b.health.Status = "killed"
	b.mu.Unlock()
	b.flattenPositions(ctx)
	b.Stop()
}

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
			b.checkExits()
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
		if pos := b.rt.Portfolio.GetPosition(sig.Symbol); pos != nil && pos.Qty.IsNegative() {
			intent.Action = strategy.ActionExitShort
		}
	}

	// Time-stop: count each incoming signal as one bar.
	if maxBars := b.tactician.MaxBarsHeld(); maxBars > 0 {
		if pos := b.rt.Portfolio.GetPosition(sig.Symbol); pos != nil && !pos.Qty.IsZero() {
			b.mu.Lock()
			b.barsSinceEntry[sig.Symbol]++
			bars := b.barsSinceEntry[sig.Symbol]
			b.mu.Unlock()
			if bars >= maxBars {
				if pos.Qty.IsPositive() {
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
		ATR:    decimal.NewFromFloat(sig.ATR),
	}, b.tactician)

	if !reply.Approved {
		b.metrics.signalsFiltered.Add(1)
		slog.Debug("bot: trade rejected", "bot_id", b.id, "symbol", sig.Symbol, "reason", reply.Reason)
		return
	}
	b.metrics.tradesApproved.Add(1)

	orderType := exchange.Market
	var limitPrice decimal.Decimal
	if reply.EntryType == "limit" && reply.LimitPrice.IsPositive() {
		orderType = exchange.Limit
		limitPrice = reply.LimitPrice
	}

	result, err := b.rt.Exchange.PlaceOrder(ctx, b.rt.Creds, exchange.OrderRequest{
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
		OrderID:        result.ID,
		BotID:          b.id,
		OrchestratorID: b.orchestratorID,
		Symbol:         sig.Symbol,
		Side:           orderbook.OrderSide(reply.Side),
		Qty:            reply.Qty,
	})

	if reply.StopLoss.IsPositive() || reply.TakeProfit.IsPositive() {
		b.mu.Lock()
		b.pendingExits[result.ID] = exitLevel{
			Side:       reply.Side,
			StopLoss:   reply.StopLoss,
			TakeProfit: reply.TakeProfit,
		}
		b.mu.Unlock()
	}

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

func (b *Bot) applyFill(orderID, symbol, side string, qty, price decimal.Decimal) {
	b.metrics.ordersFilled.Add(1)

	// Promote pending exit level to active (entry filled) or discard (close order).
	b.mu.Lock()
	if el, ok := b.pendingExits[orderID]; ok {
		delete(b.pendingExits, orderID)
		b.exitLevels[symbol] = el
	}
	b.mu.Unlock()

	if price.IsPositive() {
		if pos := b.rt.Portfolio.GetPosition(symbol); pos != nil && !pos.Qty.IsZero() {
			var pnl decimal.Decimal
			if side == "sell" && pos.Qty.IsPositive() {
				pnl = price.Sub(pos.AvgPrice).Mul(qty)
				b.mu.Lock()
				delete(b.barsSinceEntry, symbol)
				delete(b.exitLevels, symbol)
				b.mu.Unlock()
			} else if side == "buy" && pos.Qty.IsNegative() {
				pnl = pos.AvgPrice.Sub(price).Mul(qty)
				b.mu.Lock()
				delete(b.barsSinceEntry, symbol)
				delete(b.exitLevels, symbol)
				b.mu.Unlock()
			}
			if !pnl.IsZero() {
				b.metrics.mu.Lock()
				b.metrics.totalPnL = b.metrics.totalPnL.Add(pnl)
				if pnl.IsPositive() {
					b.metrics.winCount++
				} else {
					b.metrics.lossCount++
				}
				b.metrics.mu.Unlock()
			}
		}
	}

	b.rt.ReportFill(orchdomain.FillReport{
		BotID:          b.id,
		OrchestratorID: b.orchestratorID,
		OrderID:        orderID,
		Symbol:         symbol,
		Side:           side,
		Qty:            qty,
		Price:          price,
		Timestamp:      time.Now().UTC(),
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
		result, err := b.rt.Exchange.GetOrder(ctx, b.rt.Creds, o.ID)
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

// PlaceOrder submits an order via the full pipeline. Manual/legacy interface.
func (b *Bot) PlaceOrder(ctx context.Context, symbol string, qty decimal.Decimal, side string) (string, error) {
	if !b.IsRunning() {
		return "", fmt.Errorf("bot is not running")
	}
	if !qty.IsPositive() {
		return "", fmt.Errorf("invalid quantity: %s", qty)
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
	result, err := b.rt.Exchange.PlaceOrder(ctx, b.rt.Creds, exchange.OrderRequest{
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
		OrderID:        result.ID,
		BotID:          b.id,
		OrchestratorID: b.orchestratorID,
		Symbol:         symbol,
		Side:           orderbook.OrderSide(side),
		Qty:            reply.Qty,
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
