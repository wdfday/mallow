package runtime

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
)

// RiskManager is the interface for account-level risk controls.
type RiskManager interface {
	Validate(intent strategy.Intent) (bool, string)
	IsHalted() bool
	ResetHalt()
	UpdateConfig(cfg risk.Config)
}

// HelmRuntime is the live in-memory state for one helm instance.
// Holds account-level shared resources: Exchange, Portfolio, OrderBook, RiskManager.
// Per-hand resources (strategy, tactician) live on the Hand itself.
type HelmRuntime struct {
	HelmID     uuid.UUID
	AccountID  uuid.UUID
	UserID     uuid.UUID
	BrokerType string

	Portfolio *portfolio.Portfolio
	RiskMgr   RiskManager
	OrderBook orderbook.OrderBook
	Exchange  exchange.Exchange
	Creds     exchange.Credentials // per-account credentials passed to all Exchange calls

	// orderCh decouples the broker WS goroutine from NATS publishing.
	orderCh chan exchange.OrderEvent

	mu     sync.RWMutex
	bots   map[string]*Hand
	paused bool

	// lastSyncAtNano stores the last successful sync time as UnixNano (0 = never).
	lastSyncAtNano atomic.Int64

	pricesMu sync.RWMutex
	prices   map[string]decimal.Decimal // last known price per symbol

	l2Mu    sync.RWMutex
	l2Books map[string]exchange.L2Snapshot // latest books5 snapshot per symbol

	tradeMu      sync.Mutex
	requestCount atomic.Int64
	resetTicker  *time.Ticker
	stopCh       chan struct{} // closes on Stop() to terminate the circuit-breaker goroutine

	// PosLog is the durable position event log. nil = NATS unavailable (dev/test).
	PosLog poslog.Log

	// processedTrades tracks TradeIDs applied in the current session to prevent
	// double-applying gap recovery fills if RecoverGapFills runs more than once.
	processedTradesMu sync.Mutex
	processedTrades   map[string]struct{}
}

// NewHelmRuntime creates a HelmRuntime and starts its circuit-breaker reset ticker.
func NewHelmRuntime(
	orchID, accountID, userID uuid.UUID,
	brokerType string,
	pf *portfolio.Portfolio,
	riskMgr RiskManager,
	ob orderbook.OrderBook,
	ex exchange.Exchange,
	creds exchange.Credentials,
	lastSyncedAt *time.Time,
) *HelmRuntime {
	rt := &HelmRuntime{
		HelmID:          orchID,
		AccountID:       accountID,
		UserID:          userID,
		BrokerType:      brokerType,
		Portfolio:       pf,
		RiskMgr:         riskMgr,
		OrderBook:       ob,
		Exchange:        ex,
		Creds:           creds,
		orderCh:         make(chan exchange.OrderEvent, 128),
		bots:            make(map[string]*Hand),
		prices:          make(map[string]decimal.Decimal),
		processedTrades: make(map[string]struct{}),
		l2Books:         make(map[string]exchange.L2Snapshot),
		resetTicker:     time.NewTicker(1 * time.Minute),
		stopCh:          make(chan struct{}),
	}
	if lastSyncedAt != nil {
		rt.lastSyncAtNano.Store(lastSyncedAt.UnixNano())
	}
	// Reset the per-minute request counter; goroutine exits when stopCh closes.
	go func() {
		for {
			select {
			case <-rt.resetTicker.C:
				rt.requestCount.Store(0)
			case <-rt.stopCh:
				return
			}
		}
	}()
	return rt
}

// TrackOrder records a placed order in the orderbook.
func (r *HelmRuntime) TrackOrder(order orderbook.PendingOrder) {
	r.OrderBook.TrackOrder(order)
}

// Stop cleans up the circuit-breaker ticker and terminates the reset goroutine.
func (r *HelmRuntime) Stop() {
	if r.resetTicker != nil {
		r.resetTicker.Stop()
	}
	select {
	case <-r.stopCh: // already closed
	default:
		close(r.stopCh)
	}
}

// IsPaused reports whether the runtime is currently paused.
func (r *HelmRuntime) IsPaused() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.paused
}

// Pause suspends the runtime — all hands will ignore incoming signals.
// Returns IDs of hands that were running before the pause.
func (r *HelmRuntime) Pause() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = true
	var wasRunning []string
	for id, hand := range r.bots {
		hand.WasRunning = hand.IsRunning()
		if hand.WasRunning {
			wasRunning = append(wasRunning, id)
		}
	}
	return wasRunning
}

// Resume unpauses the runtime. Returns IDs of hands that should be restarted.
func (r *HelmRuntime) Resume() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = false
	var toRestart []string
	for id, hand := range r.bots {
		if hand.WasRunning {
			hand.WasRunning = false
			toRestart = append(toRestart, id)
		}
	}
	return toRestart
}

// ResetHalt clears the risk-manager halt flag on this runtime.
func (r *HelmRuntime) ResetHalt() {
	r.RiskMgr.ResetHalt()
}

// UpdateRiskConfig replaces the live risk parameters.
func (r *HelmRuntime) UpdateRiskConfig(cfg risk.Config) {
	r.RiskMgr.UpdateConfig(cfg)
}
