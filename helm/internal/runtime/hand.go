package runtime

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
	"mallow/helm/internal/runtime/position"
)

// ── Hand ──────────────────────────────────────────────────────────────────────

// Hand is an autonomous trading agent.
// Each Hand owns its own strategy, tactician, signal channel, and run-loop goroutine.
// Account-level resources (Exchange, Portfolio, RiskManager) are shared via HelmRuntime.
type Hand struct {
	// ── Identity ─────────────────────────────────────────────────────────────
	id          uuid.UUID
	helmID      uuid.UUID
	helmRuntime *HelmRuntime

	// ── Display metadata (set by service layer, read-only after Start) ────────
	Symbol       string
	StrategyName string
	Timeframe    string // bar timeframe the strategy runs on (M1/M5/H1/...); used for trade attribution
	CapitalPct   float64

	// ── Execution config (immutable after Start) ──────────────────────────────
	cfg handConfig

	// ── Strategy & tactics ────────────────────────────────────────────────────
	strategy  strategy.Strategy
	tactician tactics.Planner
	limiter   *rate.Limiter

	// ── Leverage state (futures only; nil-safe for spot) ─────────────────────
	leverageAppliedMu sync.Mutex
	leverageApplied   map[string]bool // symbols where SetLeverage has been called

	// ── Inbound channels ─────────────────────────────────────────────────────
	Signals       chan Signal   // buf=1, drain-replace; always latest non-urgent signal
	UrgentSignals chan Signal   // buf=4; exit signals, never silently dropped
	eventBus      *handEventBus // nil in production; non-nil only when EnableEventSink() is called (tests)

	// Fill mailbox — an UNBOUNDED queue, not a fixed channel. A WS fill must never be
	// dropped (a dropped fill that falls back to a direct portfolio apply bypasses the
	// hand's bracket/poslog/h.pos and silently desyncs it). EnqueueFill appends under
	// fillMu and coalesce-signals fillSignal; the run loop drains the whole queue on each
	// signal. Memory is bounded by drain speed — fast, since REST is off the actor loop.
	fillMu     sync.Mutex
	fillQueue  []exchange.WsFillEvent
	fillSignal chan struct{} // buf=1; coalescing wakeup for the run loop

	// pollCh carries the result of the off-loop REST poll batch (order + bracket states)
	// back to the run loop. A goroutine does the pure-I/O fetch and sends; the run loop
	// drains and applies on-loop — keeping the GetOrder fan-out off the actor loop so the
	// fill mailbox never starves. pollInFlight (loop-owned, no lock) guards against
	// overlapping batches.
	pollCh       chan pollBatch
	pollInFlight bool

	// placeResultCh carries an entry/exit placement back to the run loop after its REST
	// (PlaceOrder + retries) ran OFF the loop, so a slow placement never blocks fill
	// processing — the WS fill (routed by the pre-tracked clid) is drained promptly and its
	// bracket placed without waiting on the next order's REST. The loop applies the result
	// (track order, poslog, or failure cleanup) on-loop, preserving the single-owner invariant.
	placeResultCh chan *pendingPlace

	// ── Fill dedup ───────────────────────────────────────────────────────────
	// seenFills records the time each fill was applied so duplicate fill events
	// (WS + REST for the same order) are silently dropped. Entries are pruned
	// after 2 minutes — well beyond the ~100ms WS delivery window.
	seenFills map[string]time.Time

	// partialApplied accumulates incremental fill qty that the exchange pushed via
	// WS OrderEventPartialFill events and that the registry applied directly to the
	// portfolio (rt.ReportFill). When the REST poll path later sees a cumulative
	// FilledQty for the same order, it subtracts partialApplied to avoid double-counting.
	// Keyed by orderID. Deleted when the order reaches a terminal state.
	partialApplied map[string]partialAppliedState

	// ── Edge-risk guard ───────────────────────────────────────────────────────
	// guard and allocatedCap are written exclusively from the run-loop goroutine —
	// no extra lock needed beyond the run-loop single-owner invariant.
	guard        handEdgeGuard
	allocatedCap decimal.Decimal // initial budget;
	// ── Goroutine lifecycle ───────────────────────────────────────────────────
	mu        sync.RWMutex
	running   bool
	stoppedAt time.Time       // wall-clock time of last Stop(); zero = never stopped (new hand)
	ctx       context.Context // cancelled by Stop()
	cancel    context.CancelFunc
	done      chan struct{} // closed when run-loop goroutine exits

	// ── Live order & position state (all under mu) ────────────────────────────
	orders          []domain.Order
	pendingExits    map[string]exitPending  // orderID → raw SL/TP from signal; resolved on entry fill
	exitLevels      map[string]exitLevel    // symbol → active local SL/TP safety net
	pos             *position.HandPositions // in-memory mirror of poslog
	pendingOrderPos map[string]string       // orderID → positionID for fill/cancel attribution
	// pendingCancels tracks bracket/OCO order IDs that helm itself initiated a cancel for.
	// When OrderEventCanceled arrives, IDs in this set are normal cleanup (OCO sibling closed);
	// IDs NOT in this set are external cancels (user closed position manually at exchange).
	pendingCancels map[string]struct{}
	// exitCancelCh receives bracket order IDs whose cancel event arrived via WS.
	// Routed through the actor loop (not a raw goroutine) so HandleExitOrderCanceled
	// always runs single-owner alongside fills. Buffered 8 to absorb OCO sibling pairs
	// that arrive in rapid succession. A 100ms delay is applied before sending so that
	// the paired fill event (which arrives ~2ms later from Binance) can be drained first,
	// correctly populating pendingCancels before the cancel is processed.
	exitCancelCh chan string

	// ── Observability ────────────────────────────────────────────────────────
	health  HandHealth
	metrics handMetrics
}

// ID returns the bot's unique identifier.
func (h *Hand) ID() uuid.UUID { return h.id }

// DisableRateLimit replaces the signal rate limiter with an infinite-rate limiter.
// Intended for tests that perform many round trips without timing gaps; not intended
// for production use (the run-loop rate limiter is a trading-loop protection circuit).
func (h *Hand) DisableRateLimit() {
	h.limiter = rate.NewLimiter(rate.Inf, 0)
}

// realizedEquity returns the hand's capital base for sizing: AllocatedCapital
// plus net closed PnL (after commissions). Capped at zero — a hand that has
// blown through its allocation stops trading via the zero-quantity guard.
func (h *Hand) realizedEquity() decimal.Decimal {
	h.mu.RLock()
	allocatedCap := h.allocatedCap
	h.mu.RUnlock()

	h.metrics.mu.Lock()
	pnl := h.metrics.totalPnL
	commission := h.metrics.totalCommission
	h.metrics.mu.Unlock()

	realized := allocatedCap.Add(pnl).Sub(commission)
	if realized.IsPositive() {
		return realized
	}
	return decimal.Zero
}

// trackOrder records a placed order in the helm-level orderID→handID map.
func (h *Hand) trackOrder(orderID string) {
	h.helmRuntime.TrackOrder(orderID, h.id.String())
}

// partialAppliedState tracks accumulated quantities and costs for partial fills
// of a single order. Used to compute VWAP and total commission on the final fill.
type partialAppliedState struct {
	Qty        decimal.Decimal
	Cost       decimal.Decimal // sum(qty * price)
	Commission decimal.Decimal // sum(commission)
}

// exitPending holds raw SL/TP from the signal, stored in pendingExits[orderID]
// when an entry order is placed. Resolved to an exitLevel once the fill price is known.
type exitPending struct {
	Side             string
	IsOffset         bool
	StopLoss         decimal.Decimal // absolute (when !IsOffset)
	TakeProfit       decimal.Decimal // absolute (when !IsOffset)
	StopOffset       decimal.Decimal // delta from fill price (when IsOffset), e.g. -1.0 for long stop 1 point below
	TakeProfitOffset decimal.Decimal // delta from fill price (when IsOffset), e.g. +3.0 for long TP 3 points above
}

// exitLevel holds resolved SL/TP after an entry fill.
// StopLoss and TakeProfit are always absolute prices.
// Stored in exitLevels[symbol] as the local safety net while the position is open.
type exitLevel struct {
	Side             string
	StopLoss         decimal.Decimal // absolute stop price; zero = not set
	TakeProfit       decimal.Decimal // absolute take-profit price; zero = not set
	ExchangeOrderIDs []string        // exchange-side SL/TP order IDs; canceled when position closes
	PlacedAt         time.Time       // when the bracket order was placed; used for not_found grace period
}

// ── handConfig ────────────────────────────────────────────────────────────────

// handConfig holds all per-hand execution settings that are immutable after Start.
// Set once by NewHand; every field is read-only for the lifetime of the run loop.
type handConfig struct {
	pyramid         bool                  // true = merge additional entries into existing leg
	maxUnits        int                   // max concurrent legs; used by reconciler on replay
	signalTTL       time.Duration         // 0 = TTL check disabled
	orderType       domain.OrderType      //
	limitTimeoutSec int                   // 0 = no timeout
	limitFallback   domain.LimitFallback  //
	futuresConfig   *domain.FuturesConfig // nil for spot
}

// ── handEdgeGuard ─────────────────────────────────────────────────────────────

// handEdgeGuard is the sliding-window edge-risk circular buffer.
// All fields are written exclusively from the run-loop goroutine — no lock needed.
type handEdgeGuard struct {
	cfg        domain.HandGuardConfig
	ring       []decimal.Decimal // circular PnL buffer; len = cfg.WindowTrades
	head       int               // next write slot (wraps modulo cfg.WindowTrades)
	full       bool              // true once ring has wrapped at least once
	consecLoss int               // current consecutive-loss streak
}

// push appends pnl into the ring, advances head, and marks full on first wrap.
// Caller must ensure cfg.WindowTrades > 0 before calling (checkEdgeRisk guards this).
func (g *handEdgeGuard) push(pnl decimal.Decimal) {
	g.ring[g.head] = pnl
	g.head = (g.head + 1) % g.cfg.WindowTrades
	if g.head == 0 {
		g.full = true
	}
}

// ── handMetrics ───────────────────────────────────────────────────────────────

// handMetrics is the live atomic counter state embedded in Hand.
// Snapshot it via Hand.Metrics() which converts to the JSON-safe HandMetrics DTO.
type handMetrics struct {
	signalsReceived   atomic.Int64
	signalsFiltered   atomic.Int64
	signalsDropped    atomic.Int64 // non-urgent channel-full drops
	tradesApproved    atomic.Int64
	ordersPlaced      atomic.Int64
	ordersFilled      atomic.Int64
	ordersFailed      atomic.Int64
	latestSignalLagMs atomic.Int64 // lag from signal GeneratedAt → hand receives; ms

	// PnL fields use a separate mutex because decimal.Decimal is not atomic.
	mu              sync.Mutex
	totalPnL        decimal.Decimal
	totalCommission decimal.Decimal
	winCount        int64
	lossCount       int64
}
