package runtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
	"mallow/helm/internal/runtime/position"
)

// exitLevel tracks the local SL/TP levels for an open position.
// Populated when an entry order fills; cleared when the position is closed.
//
// When IsOffset=true the raw offsets are stored here and resolved to absolute
// prices at fill time using the actual fill price (see applyFill).
// Once resolved, IsOffset is cleared and StopLoss/TakeProfit hold absolute prices.
type exitLevel struct {
	Side       string          // opening side: "buy" (long) or "sell" (short)
	StopLoss   decimal.Decimal // absolute stop price; zero = not set
	TakeProfit decimal.Decimal // absolute take-profit price; zero = not set

	// Offset fields — only valid when IsOffset=true (pending exit awaiting fill price).
	IsOffset         bool
	StopOffset       decimal.Decimal // delta from fill price (e.g. -1.0 for long stop 1 point below)
	TakeProfitOffset decimal.Decimal // delta from fill price (e.g. +3.0 for long TP 3 points above)

	// ExchangeOrderIDs are the exchange-side SL/TP order IDs placed via PlaceExitOrders.
	// Cancelled automatically when the position closes (so the other leg doesn't linger).
	ExchangeOrderIDs []string
}

// l2Guard holds per-symbol warm-up state for reactive L2 monitoring.
// Prevents reactions on the first few snapshots when the hand just started.
type l2Guard struct {
	warmupLeft int // countdown; hand reacts only after this reaches zero
}

// Hand is an autonomous trading agent.
// Each Hand owns its own strategy, tactician, signal channel, and run-loop goroutine.
// Account-level resources (Exchange, Portfolio, OrderBook) are shared via HelmRuntime.
type Hand struct {
	id        uuid.UUID
	helmID    uuid.UUID
	rt        *HelmRuntime
	strategy  strategy.Strategy
	tactician tactics.Planner
	limiter   *rate.Limiter

	// Position sizing config — used by the reconciler to replay poslog correctly.
	pyramid  bool
	maxUnits int

	// signalTTL is the per-hand maximum age of a signal (measured from NATS
	// ingestion time) before it is discarded. 0 disables the check.
	signalTTL time.Duration

	// LimitTimeoutSec / LimitFallback control what happens when a limit order
	// hasn't filled after the specified number of seconds. 0 = no timeout.
	LimitTimeoutSec int
	LimitFallback   string // "cancel" (default) | "market"

	// futuresConfig holds leverage/margin type for futures hands. nil for spot.
	futuresConfig *domain.FuturesConfig

	// leverageApplied tracks which symbols have had SetLeverage called to avoid
	// redundant calls on every entry order.
	leverageAppliedMu sync.Mutex
	leverageApplied   map[string]bool

	// Signals receives regular (non-urgent) entry/exit signals.
	// Buffer=1 with drain-replace: always holds the latest signal, never stale ones.
	Signals chan Signal

	// UrgentSignals receives close/exit signals that must not be dropped.
	// Buffer=4 to absorb bursts without blocking the dispatcher.
	// Drained with priority in the run-loop before Signals.
	UrgentSignals chan Signal

	// fillCh receives fully-filled OrderEvents from the WS order processor so that
	// hand-level state (exit levels, poslog, metrics) is updated immediately without
	// waiting for the 5s REST poll cycle.
	// Buffer=8: registry never blocks; hand processes at run-loop pace.
	fillCh chan exchange.OrderEvent

	// seenFills tracks order IDs already processed via the WS fill path so that
	// the REST poll fallback does not double-apply portfolio and poslog updates.
	seenFills map[string]struct{}

	// Metadata — set by the service layer, used for runtime bookkeeping and display.
	Symbol       string
	StrategyName string
	CapitalPct   float64

	// WasRunning remembers pre-pause state so OrchestratorRuntime.Resume can restore the bot.
	WasRunning bool

	mu           sync.RWMutex
	running      bool
	paused       bool
	ctx          context.Context // cancelled when Stop() is called; used by goroutines spawned during run
	cancel       context.CancelFunc
	done         chan struct{}
	orders       []domain.Order
	pendingExits map[string]exitLevel // orderID → SL/TP levels; promoted to exitLevels on entry fill
	exitLevels   map[string]exitLevel // symbol → active SL/TP for open position (local safety net)
	l2Guards     map[string]*l2Guard  // symbol → L2 warm-up state

	// Position event log — durable write-ahead log for crash resilience.
	pos             *position.HandPositions // live in-memory position state (mirrors poslog)
	pendingOrderPos map[string]string       // orderID → positionID (for fill/cancel attribution)

	activityLog ActivityRing

	health  HandHealth
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

// DeployedCapital returns the notional capital currently committed in open positions:
// sum(leg.Qty × leg.EntryPrice) across all active legs.
func (h *Hand) DeployedCapital() decimal.Decimal {
	h.mu.RLock()
	defer h.mu.RUnlock()
	total := decimal.Zero
	for _, leg := range h.pos.ActiveLegs() {
		total = total.Add(leg.Qty.Mul(leg.EntryPrice))
	}
	return total
}

// restorePosition re-applies HandPositions reconstructed from poslog replay.
// Called by the reconciler after startup to bring the in-memory hand in sync
// with what actually happened at the exchange while the app was down.
// currentPrice comes from ListPositions and is used to seed the Portfolio.
func (b *Hand) restorePosition(hp *position.HandPositions, currentPrice decimal.Decimal) {
	pos := hp.ToPosition(b.id.String(), b.helmID.String(), currentPrice)

	// Restore each active leg into the portfolio without generating new orders.
	for _, leg := range hp.ActiveLegs() {
		b.rt.Portfolio.RestorePosition(leg.Symbol, leg.Side, leg.Qty, leg.EntryPrice, currentPrice)
	}

	b.mu.Lock()
	// Take ownership of the replayed position state.
	b.pos = hp

	// Rebuild pending order → position mapping for legs still awaiting a fill.
	for _, leg := range hp.ActiveLegs() {
		if leg.HasPendingOrder() {
			b.pendingOrderPos[leg.PendingOrderID] = leg.PositionID
		}
	}

	// Rebuild local exit-level guards.
	// Pyramid: SL/TP from pos (latest signal levels); non-pyramid: per-leg.
	if b.pyramid {
		if pos.StopLoss.IsPositive() || pos.TakeProfit.IsPositive() {
			b.exitLevels[pos.Symbol] = exitLevel{
				Side:       pos.Side,
				StopLoss:   pos.StopLoss,
				TakeProfit: pos.TakeProfit,
			}
		}
	} else {
		for _, leg := range hp.ActiveLegs() {
			if leg.StopLoss.IsPositive() || leg.TakeProfit.IsPositive() {
				b.exitLevels[leg.Symbol] = exitLevel{
					Side:       leg.Side,
					StopLoss:   leg.StopLoss,
					TakeProfit: leg.TakeProfit,
				}
			}
		}
	}
	b.mu.Unlock()
}

// ID returns the bot's unique identifier.
func (b *Hand) ID() uuid.UUID { return b.id }

func timePtr(t time.Time) *time.Time { return &t }

func unmarshalJSON(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

// RecordDrop increments the dropped-signal counter. Called by the dispatcher.
func (b *Hand) RecordDrop() { b.metrics.signalsDropped.Add(1) }

// DeliverSignal enqueues a signal onto the appropriate hand channel.
func (b *Hand) DeliverSignal(sig Signal) {
	if sig.IsUrgent() {
		select {
		case b.UrgentSignals <- sig:
		default:
			slog.Error("hand urgent signal channel full, dropping close signal",
				"hand_id", b.id, "symbol", sig.Symbol)
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
		slog.Warn("hand signal channel full after drain, dropping",
			"hand_id", b.id, "symbol", sig.Symbol)
	}
}

// NewHand creates a Hand. Call Start() to spawn its run-loop goroutine.
func NewHand(
	id, helmID uuid.UUID,
	rt *HelmRuntime,
	strat strategy.Strategy,
	tact tactics.Planner,
	pyramid bool,
	maxUnits int,
	signalTTL time.Duration,
	futuresConfig *domain.FuturesConfig,
) *Hand {
	if maxUnits <= 0 {
		maxUnits = 1
	}
	return &Hand{
		id:              id,
		helmID:          helmID,
		rt:              rt,
		strategy:        strat,
		tactician:       tact,
		limiter:         rate.NewLimiter(rate.Every(1*time.Second), 5),
		pyramid:         pyramid,
		maxUnits:        maxUnits,
		futuresConfig:   futuresConfig,
		signalTTL:       signalTTL,
		leverageApplied: make(map[string]bool),
		Signals:         make(chan Signal, 1),
		UrgentSignals:   make(chan Signal, 4),
		fillCh:          make(chan exchange.OrderEvent, 8),
		seenFills:       make(map[string]struct{}),
		orders:          make([]domain.Order, 0, 256),
		pendingExits:    make(map[string]exitLevel),
		exitLevels:      make(map[string]exitLevel),
		l2Guards:        make(map[string]*l2Guard),
		pos:             position.NewHandPositions(pyramid, maxUnits),
		pendingOrderPos: make(map[string]string),
		health:          HandHealth{Status: "stopped"},
	}
}

// SignalTTLFor returns the per-hand signal TTL from domain config.
// Default is 10s when SignalTTLSec is 0; negative values disable the check.
func SignalTTLFor(b *domain.Hand) time.Duration {
	switch {
	case b.Risk.SignalTTLSec < 0:
		return 0
	case b.Risk.SignalTTLSec > 0:
		return time.Duration(b.Risk.SignalTTLSec) * time.Second
	default:
		return 10 * time.Second
	}
}

// BuildHandComponents translates a Hand into a Strategy + Tactician.
func BuildHandComponents(b *domain.Hand) (strategy.Strategy, *tactics.Tactician) {
	minStrength := b.Strategy.MinStrength
	if minStrength <= 0 {
		minStrength = 0.3
	}

	strat := strategy.NewSignalFollower(minStrength)

	sizingMode := tactics.SizingFixedFractional
	switch b.Position.SizeMode {
	case "fixed_qty":
		sizingMode = tactics.SizingFixedQty
	case "quote_qty":
		sizingMode = tactics.SizingQuoteQty
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
		MaxPositions:     b.Position.MaxUnits,
		RiskPerTradePct:  b.Position.RiskPerTradePct,
		MaxPositionPct:   b.Position.MaxPositionPct,
		FixedQty:         b.Position.FixedQty,
		FixedQuoteQty:    b.Position.FixedQuoteQty,
	}
	tact := tactics.New(sc)

	return strat, tact
}

// Start spawns the bot's run-loop goroutine.
func (b *Hand) Start() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return
	}
	b.running = true
	b.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	b.ctx = ctx
	b.cancel = cancel
	b.health.Status = "running"
	b.health.StartedAt = timePtr(time.Now().UTC())
	go b.run(ctx)
	slog.Info("hand started", "hand_id", b.id, "exchange", b.rt.Exchange.Name())
}

// applyFuturesLeverage calls SetLeverage on the exchange if the hand is configured
// for futures and the exchange supports it. Non-blocking on failure (just logs).
func (b *Hand) applyFuturesLeverage(ctx context.Context, symbol string, futures *domain.FuturesConfig) {
	if futures == nil || futures.Leverage <= 0 {
		return
	}
	setter, ok := b.rt.Exchange.(exchange.LeverageSetter)
	if !ok {
		return
	}
	marginType := futures.MarginType
	if marginType == "" {
		marginType = "isolated"
	}
	if err := setter.SetLeverage(ctx, b.rt.Creds, symbol, futures.Leverage, marginType); err != nil {
		slog.Warn("hand: set leverage failed (non-fatal)", "hand_id", b.id, "symbol", symbol,
			"leverage", futures.Leverage, "margin_type", marginType, "err", err)
	} else {
		slog.Info("hand: leverage set", "hand_id", b.id, "symbol", symbol,
			"leverage", futures.Leverage, "margin_type", marginType)
	}
}

// Stop cancels the run-loop and waits for it to exit.
func (b *Hand) Stop() {
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
	slog.Info("hand stopped", "hand_id", b.id)
}

func (b *Hand) IsRunning() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.running
}

// IsPaused reports whether the hand is individually paused.
func (b *Hand) IsPaused() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.paused
}

// Pause suspends signal processing for this hand without stopping its goroutine.
func (b *Hand) Pause() {
	b.mu.Lock()
	b.paused = true
	b.health.Status = "paused"
	b.mu.Unlock()
	slog.Info("hand paused", "hand_id", b.id)
}

// Resume re-enables signal processing after a Pause.
func (b *Hand) Resume() {
	b.mu.Lock()
	if b.running {
		b.paused = false
		b.health.Status = "running"
	}
	b.mu.Unlock()
	slog.Info("hand resumed", "hand_id", b.id)
}

// Kill stops the hand and immediately closes all open positions via market orders.
func (b *Hand) Kill(ctx context.Context) {
	slog.Warn("bot: kill initiated — flattening all positions", "hand_id", b.id)
	b.mu.Lock()
	b.paused = true
	b.health.Status = "killed"
	b.mu.Unlock()
	b.flattenPositions(ctx)
	b.Stop()
}

// Release stops the hand without closing open positions.
// Each open leg is emitted as KindPositionOrphaned so the reconciler never
// reclaims it on restart. The position stays live at the exchange with any
// exchange-side SL/TP already placed.
func (b *Hand) Release(ctx context.Context) {
	slog.Info("bot: release — orphaning open positions", "hand_id", b.id)
	b.mu.Lock()
	b.paused = true
	b.health.Status = "released"
	b.mu.Unlock()
	b.releasePositions(ctx)
	b.Stop()
}

// Orders returns a snapshot of this bot's submitted orders.
func (b *Hand) Orders() []domain.Order {
	b.mu.RLock()
	defer b.mu.RUnlock()
	result := make([]domain.Order, len(b.orders))
	copy(result, b.orders)
	return result
}

// Health returns a snapshot of the bot's health state.
func (b *Hand) Health() HandHealth {
	b.mu.RLock()
	defer b.mu.RUnlock()
	h := b.health
	if b.running && b.health.StartedAt != nil {
		h.Uptime = time.Since(*b.health.StartedAt).Truncate(time.Second).String()
	}
	return h
}

// Metrics returns a snapshot of the bot's trading metrics.
func (b *Hand) Metrics() HandMetrics {
	b.metrics.mu.Lock()
	defer b.metrics.mu.Unlock()
	return HandMetrics{
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

// EnqueueFill forwards a fully-filled WS OrderEvent to the hand's run-loop.
// Non-blocking: if the buffer is full, the fill will be picked up by the REST poll
// fallback instead. Called by the registry fill processor from its own goroutine.
func (b *Hand) EnqueueFill(ev exchange.OrderEvent) {
	select {
	case b.fillCh <- ev:
	default:
		slog.Warn("hand: fill channel full, REST poll will handle",
			"hand_id", b.id, "order_id", ev.OrderID)
	}
}

// Activity returns a chronological snapshot of the hand's recent activity log.
func (b *Hand) Activity() []ActivityEntry { return b.activityLog.Snapshot() }

func (b *Hand) recordActivity(e ActivityEntry) { b.activityLog.push(e) }

// trackOrder records a placed order in the shared orderbook.
func (b *Hand) trackOrder(o orderbook.PendingOrder) {
	b.rt.TrackOrder(o)
}
