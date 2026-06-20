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
	"mallow/helm/internal/safe"
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
	SetAvailableCashFn(fn func() decimal.Decimal)
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
	Portfolio   *portfolio.Portfolio
	RiskMgr     RiskManager
	Exchange    exchange.Exchange
	Creds       exchange.Credentials
	FilterStore SymbolFilterStore // registry-owned symbol precision cache; nil = no prewarm

	// ── Hands ────────────────────────────────────────────────────────────────
	mu          sync.RWMutex
	hands       map[string]*Hand
	paused      bool
	pausedHands []string // hand IDs that were running when helm was paused; restored on Resume

	// ── Fill routing ─────────────────────────────────────────────────────────
	// Unbounded mailboxes + coalescing signals — mirrors hand.fillQueue pattern.
	// The WS goroutine NEVER blocks: EnqueueWsFill / EnqueueLifecycleEvent
	// append under a short mutex and signal a buf=1 channel. runFillProcessor
	// and runLifecycleProcessor drain the full batch on each wake-up.
	// This prevents the WS receive loop from stalling when runFillProcessor is
	// slow (e.g. tradeMu contention), which would otherwise back-pressure through
	// the msgs channel into TCP, delaying lifecycle events on the same connection.
	lifecycleMu     sync.Mutex
	lifecycleQueue  []exchange.OrderLifecycleEvent
	lifecycleSignal chan struct{} // buf=1 coalescing wakeup

	wsFillMu     sync.Mutex
	wsFillQueue  []exchange.WsFillEvent
	wsFillSignal chan struct{} // buf=1 coalescing wakeup
	// router owns the orderID→handID routing map (see helpers.go / orderRouter).
	router *orderRouter

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
	// prices is the registry-owned per-exchange price map wired at Spawn().
	marketData *exchangePublicData

	// ── Trade gate (per-minute circuit breaker) ───────────────────────────────
	tradeMu      sync.Mutex   // serialises ProcessTrade + ReportFill across all hands
	requestCount atomic.Int64 // resets every minute via resetTicker goroutine
	resetTicker  *time.Ticker
	stopCh       chan struct{} // closed by Stop() to exit background goroutines

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
	// fillDrainCancel cancels the appCtx used by runFillProcessor / runLifecycleProcessor
	// (set only when StartFillStreaming starts the drain goroutines).
	fillStreamMu     sync.Mutex
	fillStreamCancel context.CancelFunc
	fillDrainCancel  context.CancelFunc

	// authErrStreak counts consecutive ErrClassAuth responses from PlaceOrder.
	// Reset to 0 on any successful PlaceOrder. TriggerAuthError fires only after
	// authErrThreshold consecutive failures to tolerate transient 401s (clock skew, exchange glitch).
	authErrStreak atomic.Int32

	// onCredentialError is called (once, in a goroutine) when the exchange rejects our
	// credentials mid-run (ErrClassAuth from PlaceOrder or WS auth failure). The runtime
	// self-pauses first; the callback is responsible for persisting the error state in DB.
	// Set by the registry at spawn time via Registry.SetCredentialErrorHook.
	onCredentialError func(accountID uuid.UUID, reason string)

	// ── Fill idempotency ─────────────────────────────────────────────────────
	// dedup owns the gap-recovery trade-id set and the REST-fill-published order-id set,
	// preventing double-apply / double-publish of the same fill (see fillDedup).
	dedup *fillDedup

	// ── Event counters ───────────────────────────────────────────────────────
	// eventCounts accumulates per-code event totals for /metrics.
	// Keys are int (event code constant); values are *atomic.Int64.
	eventCounts sync.Map
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
		HelmID:          orchID,
		AccountID:       accountID,
		UserID:          userID,
		BrokerType:      brokerType,
		CreatedAt:       createdAt,
		Portfolio:       pf,
		RiskMgr:         riskMgr,
		Exchange:        ex,
		Creds:           creds,
		lifecycleSignal: make(chan struct{}, 1),
		wsFillSignal:    make(chan struct{}, 1),
		hands:           make(map[string]*Hand),
		router:          newOrderRouter(),
		marketData:      newExchangePublicData(), // default; overwritten by Registry.Spawn with shared bucket
		dedup:           newFillDedup(),
		resetTicker:     time.NewTicker(1 * time.Minute),
		stopCh:          make(chan struct{}),
	}
	if lastSyncedAt != nil {
		rt.lastSyncAtNano.Store(lastSyncedAt.UnixNano())
	}
	// Reset the per-minute request counter; goroutine exits when stopCh closes.
	go func() {
		defer safe.Recover()
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

	// Increment per-code event counter for /metrics.
	v, _ := r.eventCounts.LoadOrStore(ev.Code, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)

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

// EventCodeCounts returns a snapshot of per-event-code totals since the runtime started.
// Keys are event code constants (e.g. CodeSignalReceived = 10000); values are counts.
func (r *HelmRuntime) EventCodeCounts() map[int]int64 {
	m := make(map[int]int64)
	r.eventCounts.Range(func(k, v any) bool {
		m[k.(int)] = v.(*atomic.Int64).Load()
		return true
	})
	return m
}

// ReconcileHand runs on-demand reconciliation for a single hand.
// Called by Hand.Start() when the hand is restarted after an extended downtime gap
// (> onDemandReconcileGap) so fills and position changes during downtime are applied
// before the first signal arrives. No-op when PosLog is nil (dev / no-persistence mode).
func (r *HelmRuntime) ReconcileHand(ctx context.Context, hand *Hand) {
	if r.PosLog == nil {
		return
	}
	rec := NewReconciler(r.PosLog)
	rec.ReconcileSingle(ctx, r, hand)
}
