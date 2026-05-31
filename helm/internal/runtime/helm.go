package runtime

import (
	"context"
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

// HandPnLSummer queries aggregate PnL metrics for a hand from PostgreSQL in one query.
// Implemented by tradelog.Log; injected into HelmRuntime to avoid draining JetStream
// history on every startup.
type HandPnLSummer interface {
	SumHandPnL(ctx context.Context, handID uuid.UUID) (totalPnL, totalCommission decimal.Decimal, wins, losses int64, err error)
}

// RiskManager is the interface for account-level risk controls.
type RiskManager interface {
	Validate(intent strategy.Intent, handID string) (bool, string)
	IsHalted() bool
	ResetHalt()
	UpdateConfig(cfg risk.Config)
	SetUnitCounter(fn func() int)
}

// HelmRuntime is the live in-memory state for one helm instance.
// Holds account-level shared resources: Exchange, Portfolio, RiskManager.
// Per-hand resources (strategy, tactician) live on the Hand itself.
type HelmRuntime struct {
	// ── Identity ─────────────────────────────────────────────────────────────
	HelmID     uuid.UUID
	AccountID  uuid.UUID
	UserID     uuid.UUID
	BrokerType string
	// CreatedAt is the wall-clock time when this Helm was first configured.
	// Used as a lower-bound sentinel by gap recovery and the startup reconciler:
	// fills that occurred before this timestamp belong to a prior account configuration
	// and must never be applied to this helm's portfolio.
	CreatedAt time.Time

	// ── Core resources (account-level, shared across all hands) ──────────────
	Portfolio *portfolio.Portfolio
	RiskMgr   RiskManager
	Exchange  exchange.Exchange
	Creds     exchange.Credentials

	// ── Hands ────────────────────────────────────────────────────────────────
	mu          sync.RWMutex
	hands       map[string]*Hand
	paused      bool
	pausedHands []string // hand IDs that were running when helm was paused; restored on Resume

	// ── Fill routing ─────────────────────────────────────────────────────────
	// lifecycleCh and wsFillCh decouple WS callbacks from NATS publishing.
	// runOrderProcessor drains lifecycleCh; runFillProcessor drains wsFillCh.
	// Both enqueue methods are non-blocking (drop on full with an error log).
	lifecycleCh    chan exchange.OrderLifecycleEvent
	wsFillCh       chan exchange.WsFillEvent
	orderHandMap   map[string]string // orderID → handID; cleared on fill or cancel
	orderHandMapMu sync.RWMutex

	// fillRoute* count how WS fills resolved to a hand, for rollout observability of the
	// client-order-id migration (see CLIENT_ORDER_ID.md):
	//   clid   — routed via the mallow-generated client id (race-free path; the goal)
	//   alias  — routed via the exchange-id alias (adapter didn't echo our clid, or bracket)
	//   orphan — no owning hand (manual order, or a genuine routing miss)
	// Exposed as helm_fill_route_total{route=...} on /metrics.
	fillRouteClid   atomic.Int64
	fillRouteAlias  atomic.Int64
	fillRouteOrphan atomic.Int64

	// ── Market data cache ────────────────────────────────────────────────────
	lastSyncAtNano atomic.Int64 // UnixNano of last successful REST sync; 0 = never
	pricesMu       sync.RWMutex
	prices         map[string]decimal.Decimal

	// getL2 delegates L2 lookups to the registry's shared broker-level cache.
	// Injected at Spawn(); nil = no L2 streamer connected (ok=false on all lookups).
	getL2 func(symbol string) (exchange.L2Snapshot, bool)

	// ── Trade gate (per-minute circuit breaker) ───────────────────────────────
	tradeMu      sync.Mutex   // serialises ProcessTrade + ReportFill across all hands
	requestCount atomic.Int64 // resets every minute via resetTicker goroutine
	resetTicker  *time.Ticker
	stopCh       chan struct{} // closed by Stop() to exit background goroutines

	// ── Snapshot hint ────────────────────────────────────────────────────────
	// snapshotDirty is set to 1 by MarkSnapshotDirty() (called after fills)
	// so the snapshot loop emits sooner than snapshotHeartbeat.
	// The snapshot goroutine owns correctness; fills only provide a timing hint.
	snapshotDirty atomic.Int32

	// syncScheduled is 1 while a debounced post-order REST sync is pending.
	// Set by MarkSyncDirty (after fills); coalesces a fill burst into a single sync.
	syncScheduled atomic.Int32

	// ── Durability ───────────────────────────────────────────────────────────
	// nil fields degrade gracefully: poslog events are lost, events go to slog only.
	PosLog    poslog.Log            // JetStream WAL for position events
	TradeLog  perf.TradeLog         // JetStream HELM_TRADES — closed round-trip trades; TradePersister drains into PG
	PnLSummer HandPnLSummer         // postgres aggregate query for RestorePnL; nil = fallback to JetStream drain
	syncStore SyncStore             // persists last_synced_at after each successful portfolio sync
	nc        *nats.Conn            // NATS connection; used for portfolio.synced.* (nc.Publish path)
	js        nats.JetStreamContext // JetStream context; publishes helm.events.* (durable, 7d)

	// ── WS fill stream lifecycle ─────────────────────────────────────────────
	// fillStreamCancel cancels the per-runtime WS stream context so RotateCreds
	// can disconnect and reconnect with new credentials without touching hands.
	fillStreamMu     sync.Mutex
	fillStreamCancel context.CancelFunc

	// ── Gap-recovery dedup ───────────────────────────────────────────────────
	// Prevents double-applying the same fill if RecoverGapFills runs more than once.
	processedTradesMu sync.Mutex
	processedTrades   map[string]struct{}

	// ── REST-fill publish dedup ──────────────────────────────────────────────
	// processedOrderFills tracks orderIDs for which trade.filled was already
	// published via the REST fill path (applyFill, source != "ws").
	// This prevents Sync() from re-publishing the same fill 5 min later with a
	// different Nats-Msg-Id, which would create duplicate fill events for consumers.
	processedOrderFillsMu sync.Mutex
	processedOrderFills   map[string]struct{}

	// ── Dust tracking ────────────────────────────────────────────────────────
	// dustMu guards dustQty. Dust accumulates when a spot exit order is placed
	// with qty truncated to the exchange lot-size step, leaving a sub-step residual
	// in the wallet (e.g. 0.0944055 → sell 0.0944 → dust 0.0000055 ETH).
	// checkPositionDesync uses this to distinguish genuine external closes from
	// known dust residuals after a helm-initiated exit.
	dustMu  sync.Mutex
	dustQty map[string]decimal.Decimal // symbol → accumulated dust qty
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
	createdAt time.Time,
) *HelmRuntime {
	rt := &HelmRuntime{
		HelmID:              orchID,
		AccountID:           accountID,
		UserID:              userID,
		BrokerType:          brokerType,
		CreatedAt:           createdAt,
		Portfolio:           pf,
		RiskMgr:             riskMgr,
		Exchange:            ex,
		Creds:               creds,
		lifecycleCh:         make(chan exchange.OrderLifecycleEvent, 128),
		wsFillCh:            make(chan exchange.WsFillEvent, 256),
		orderHandMap:        make(map[string]string),
		hands:               make(map[string]*Hand),
		prices:              make(map[string]decimal.Decimal),
		processedTrades:     make(map[string]struct{}),
		processedOrderFills: make(map[string]struct{}),
		dustQty:             make(map[string]decimal.Decimal),
		resetTicker:         time.NewTicker(1 * time.Minute),
		stopCh:              make(chan struct{}),
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

// SetEventConn injects the NATS connection and JetStream context.
// nc is used for portfolio.synced.* publishes; js is used for helm.events.* (durable).
// nil = slog-only mode (dev/test).
func (r *HelmRuntime) SetEventConn(nc *nats.Conn, js nats.JetStreamContext) {
	r.nc = nc
	r.js = js
}

// EmitEvent logs a behavioral event via slog.Info and, when NATS is available,
// publishes it to helm.events.{helmID} so clients receive a real-time activity stream.
func (r *HelmRuntime) EmitEvent(ev natsapi.HelmEvent) {
	ev.HelmID = r.HelmID.String()
	ev.UserID = r.UserID.String()
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

	if r.js != nil {
		natsapi.PublishHelmEvent(r.js, r.HelmID.String(), ev)
	}
}
