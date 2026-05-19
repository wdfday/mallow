package runtime

import (
	"context"
	"encoding/json"
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
	ExchangeOrderIDs []string        // exchange-side SL/TP order IDs; cancelled when position closes
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

	// orderType is the default entry order type (market or limit).
	orderType domain.OrderType

	// limitTimeoutSec / limitFallback control what happens when a limit order
	// hasn't filled after the specified number of seconds. 0 = no timeout.
	limitTimeoutSec int
	limitFallback   domain.LimitFallback

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

	// Edge-degradation guard — sliding window over closed trades.
	// All fields are written exclusively from the run-loop goroutine; no extra lock needed.
	edgeRisk     domain.HandRiskConfig
	allocatedCap decimal.Decimal   // reference capital for pct thresholds; falls back to portfolio equity
	tradeRing    []decimal.Decimal // circular buffer of per-trade PnL, len = edgeRisk.WindowTrades
	ringHead     int               // next write slot
	ringFull     bool              // true once the ring has been filled at least once
	consecLoss   int               // current consecutive-loss streak

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
	pendingExits map[string]exitPending // orderID → raw SL/TP from signal; resolved to exitLevel on entry fill
	exitLevels   map[string]exitLevel   // symbol → resolved SL/TP for open position (local safety net)

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

// ID returns the bot's unique identifier.
func (h *Hand) ID() uuid.UUID { return h.id }

// realizedEquity returns the hand's actual capital base for sizing:
// AllocatedCapital + all closed PnL so far. Returns zero when AllocatedCapital
// is not set, signalling ProcessTrade to fall back to full portfolio equity.
// Capped at zero — a hand that has blown through its allocation stops trading
// via the zero-quantity guard in ProcessTrade, not by going negative.
func (h *Hand) realizedEquity() decimal.Decimal {
	if !h.allocatedCap.IsPositive() {
		return decimal.Zero
	}
	h.metrics.mu.Lock()
	pnl := h.metrics.totalPnL
	h.metrics.mu.Unlock()
	realized := h.allocatedCap.Add(pnl)
	if realized.IsPositive() {
		return realized
	}
	return decimal.Zero
}

func timePtr(t time.Time) *time.Time { return &t }

func unmarshalJSON(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

func (h *Hand) recordActivity(e ActivityEntry) { h.activityLog.push(e) }

// trackOrder records a placed order in the helm-level orderID→handID map.
func (h *Hand) trackOrder(orderID string) {
	h.rt.TrackOrder(orderID, h.id.String())
}
