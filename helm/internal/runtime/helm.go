package runtime

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/perf"
)

// RiskManager is the interface for account-level risk controls.
type RiskManager interface {
	Validate(intent strategy.Intent) (bool, string)
	IsHalted() bool
	ResetHalt()
	UpdateConfig(cfg risk.Config)
	SetUnitCounter(fn func() int)
}

// HelmRuntime is the live in-memory state for one helm instance.
// Holds account-level shared resources: Exchange, Portfolio, RiskManager.
// Per-hand resources (strategy, tactician) live on the Hand itself.
type HelmRuntime struct {
	HelmID     uuid.UUID
	AccountID  uuid.UUID
	UserID     uuid.UUID
	BrokerType string

	Portfolio *portfolio.Portfolio
	RiskMgr   RiskManager
	Exchange  exchange.Exchange
	Creds     exchange.Credentials // per-account credentials passed to all Exchange calls

	// orderCh decouples the broker WS goroutine from NATS publishing.
	orderCh chan exchange.OrderEvent

	// orderHandMap maps orderID → handID for WS fill routing.
	// Populated by TrackOrder when a hand places an order; cleared on fill or cancel.
	orderHandMap   map[string]string
	orderHandMapMu sync.RWMutex

	mu     sync.RWMutex
	hands  map[string]*Hand
	paused bool

	// lastSyncAtNano stores the last successful sync time as UnixNano (0 = never).
	lastSyncAtNano atomic.Int64

	pricesMu sync.RWMutex
	prices   map[string]decimal.Decimal // last known price per symbol
	l2Mu     sync.RWMutex
	l2       map[string]exchange.L2Snapshot // latest L2 snapshot per symbol (shared across all hands)

	tradeMu      sync.Mutex
	requestCount atomic.Int64
	resetTicker  *time.Ticker
	stopCh       chan struct{} // closes on Stop() to terminate the circuit-breaker goroutine

	// PosLog is the durable position event log. nil = NATS unavailable (dev/test).
	PosLog poslog.Log

	// PortfolioLog records raw portfolio state (cash + positions) after every fill.
	// nil = NATS unavailable (dev/test). FE uses these snapshots to compute equity
	// at any timeframe without needing pre-multiplied values.
	PortfolioLog perf.PortfolioLog

	// nc is used for real-time activity event publishing (helm.events.{helmID}).
	// nil = NATS unavailable (dev/test) — EmitEvent degrades to slog-only.
	nc *nats.Conn

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
		Exchange:        ex,
		Creds:           creds,
		orderCh:         make(chan exchange.OrderEvent, 128),
		orderHandMap:    make(map[string]string),
		hands:           make(map[string]*Hand),
		prices:          make(map[string]decimal.Decimal),
		l2:              make(map[string]exchange.L2Snapshot),
		processedTrades: make(map[string]struct{}),
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

// SetEventConn injects the NATS connection used for real-time event publishing.
// Called after construction from registry_lifecycle.go, same pattern as PosLog.
// nil = slog-only mode (dev/test).
func (r *HelmRuntime) SetEventConn(nc *nats.Conn) { r.nc = nc }

// EmitEvent logs a behavioral event via slog.Info and, when NATS is available,
// publishes it to helm.events.{helmID} so clients receive a real-time activity stream.
func (r *HelmRuntime) EmitEvent(ev natsapi.HelmEvent) {
	ev.HelmID = r.HelmID.String()
	ev.At = time.Now().UTC()

	args := []any{"helm_id", r.HelmID, "code", ev.Code}
	if ev.HandID != "" {
		args = append(args, "hand_id", ev.HandID)
	}
	if ev.Symbol != "" {
		args = append(args, "symbol", ev.Symbol)
	}
	if ev.OrderID != "" {
		args = append(args, "order_id", ev.OrderID)
	}
	if ev.Qty.IsPositive() {
		args = append(args, "qty", ev.Qty)
	}
	if ev.Price.IsPositive() {
		args = append(args, "price", ev.Price)
	}
	if ev.Reason != "" {
		args = append(args, "reason", ev.Reason)
	}

	slog.Info(ev.Msg, args...)

	if r.nc != nil {
		natsapi.PublishHelmEvent(r.nc, r.HelmID.String(), ev)
	}
}
