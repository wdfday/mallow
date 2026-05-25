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
}

// Hand is an autonomous trading agent.
// Each Hand owns its own strategy, tactician, signal channel, and run-loop goroutine.
// Account-level resources (Exchange, Portfolio, RiskManager) are shared via HelmRuntime.
type Hand struct {
	// ── Identity ─────────────────────────────────────────────────────────────
	id          uuid.UUID
	helmID      uuid.UUID
	helmRuntime *HelmRuntime

	// ── Strategy & tactics ───────────────────────────────────────────────────
	strategy  strategy.Strategy
	tactician tactics.Planner
	limiter   *rate.Limiter

	// ── Execution config (immutable after Start) ──────────────────────────────
	pyramid         bool          // true = merge additional entries into existing leg
	maxUnits        int           // max concurrent legs; used by reconciler on replay
	signalTTL       time.Duration // 0 = TTL check disabled
	orderType       domain.OrderType
	limitTimeoutSec int // 0 = no timeout
	limitFallback   domain.LimitFallback
	futuresConfig   *domain.FuturesConfig // nil for spot

	leverageAppliedMu sync.Mutex
	leverageApplied   map[string]bool // symbols where SetLeverage has been called

	// ── Inbound channels ─────────────────────────────────────────────────────
	Signals       chan Signal              // buf=1, drain-replace; always latest non-urgent signal
	UrgentSignals chan Signal              // buf=4; exit signals, never silently dropped
	fillCh        chan exchange.OrderEvent // buf=8; WS fills routed from runOrderProcessor
	eventBus      *handEventBus            // nil in production; non-nil only when EnableEventSink() is called (tests)

	seenFills map[string]struct{} // dedup: WS-applied fills vs REST poll fallback

	// ── Edge-risk guard ───────────────────────────────────────────────────────
	// All fields written exclusively from the run-loop goroutine — no extra lock.
	edgeRisk     domain.HandRiskConfig
	allocatedCap decimal.Decimal   // initial budget; zero = fall back to full portfolio equity
	tradeRing    []decimal.Decimal // circular PnL buffer, len = edgeRisk.WindowTrades
	ringHead     int               // next write slot in tradeRing
	ringFull     bool              // true after tradeRing has wrapped at least once
	consecLoss   int               // current consecutive-loss streak

	// ── Display metadata (set by service layer, read-only after Start) ────────
	Symbol       string
	StrategyName string
	CapitalPct   float64
	WasRunning   bool // pre-pause state; read by Resume to decide whether to restart

	// ── Goroutine lifecycle ───────────────────────────────────────────────────
	mu      sync.RWMutex
	running bool
	paused  bool
	ctx     context.Context // cancelled by Stop()
	cancel  context.CancelFunc
	done    chan struct{} // closed when run-loop goroutine exits

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

	// ── Observability ────────────────────────────────────────────────────────
	health  HandHealth
	metrics struct {
		signalsReceived   atomic.Int64
		signalsFiltered   atomic.Int64
		signalsDropped    atomic.Int64 // non-urgent channel-full drops
		tradesApproved    atomic.Int64
		ordersPlaced      atomic.Int64
		ordersFilled      atomic.Int64
		ordersFailed      atomic.Int64
		latestSignalLagMs atomic.Int64 // lag from signal GeneratedAt → hand receives; ms
		mu                sync.Mutex
		totalPnL          decimal.Decimal
		totalCommission   decimal.Decimal
		winCount          int64
		lossCount         int64
	}
}

// ID returns the bot's unique identifier.
func (h *Hand) ID() uuid.UUID { return h.id }

// realizedEquity returns the hand's capital base for sizing: AllocatedCapital
// plus all closed PnL so far. Capped at zero — a hand that has blown through
// its allocation stops trading via the zero-quantity guard in ProcessTrade.
func (h *Hand) realizedEquity() decimal.Decimal {
	h.mu.RLock()
	allocatedCap := h.allocatedCap
	h.mu.RUnlock()

	h.metrics.mu.Lock()
	pnl := h.metrics.totalPnL
	h.metrics.mu.Unlock()

	realized := allocatedCap.Add(pnl)
	if realized.IsPositive() {
		return realized
	}
	return decimal.Zero
}

// trackOrder records a placed order in the helm-level orderID→handID map.
func (h *Hand) trackOrder(orderID string) {
	h.helmRuntime.TrackOrder(orderID, h.id.String())
}
