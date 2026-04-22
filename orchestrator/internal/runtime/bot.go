package runtime

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/shopspring/decimal"

	"orchestrator/internal/module/bot/domain"
	"orchestrator/internal/runtime/core/orderbook"
	"orchestrator/internal/runtime/core/strategy"
	"orchestrator/internal/runtime/core/tactics"
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

// Orders returns a snapshot of this bot's submitted orders.
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

// trackOrder records a placed order in the shared orderbook.
func (b *Bot) trackOrder(o orderbook.PendingOrder) {
	b.rt.TrackOrder(o)
}
