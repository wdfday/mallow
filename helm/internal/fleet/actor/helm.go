package actor

import (
	"context"
	"log/slog"
	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/risk"
	"mallow/helm/internal/fleet/actor/core/strategy"
	signalfollower "mallow/helm/internal/fleet/actor/signal-follower"
	"mallow/helm/internal/fleet/market"
	"mallow/helm/internal/fleet/perf"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/infra/natsapi"
	handdomain "mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/safe"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"
)

// handEntry pairs the live Hand goroutine with its persisted domain data.
// Owned exclusively by HelmRuntime; never shared across runtimes.
type handEntry struct {
	h    *signalfollower.Hand
	data *handdomain.Hand
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
	FilterStore market.SymbolFilterStore // registry-owned symbol precision cache; nil = no prewarm
	Herald      HeraldRegistrar          // nil when NATS/herald unavailable (dev/test)

	// ── Hands ────────────────────────────────────────────────────────────────
	mu          sync.RWMutex
	hands       map[string]*handEntry
	Paused      bool
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
	MarketData *market.ExchangeData

	// ── Trade gate (per-minute circuit breaker) ───────────────────────────────
	requestCount atomic.Int64 // resets every minute via resetTicker goroutine
	resetTicker  *time.Ticker
	stopCh       chan struct{} // closed by Stop() to exit background goroutines

	// ── Trade actor (see helm_actor.go) ───────────────────────────────────────
	// runTradeActor is the single goroutine that owns Portfolio/leverage mutation —
	// replaces the old tradeMu mutex. Started unconditionally in NewHelmRuntime.
	tradeReqCh    chan *tradeRequest
	fillReportCh  chan helmdomain.FillReport
	leverageReqCh chan *leverageRequest
	feeEventCh    chan FeeEvent
	// leverageSet tracks which symbols already had SetLeverage applied for this
	// helm's account — actor-owned (only runTradeActor touches it), replacing the
	// old per-Hand leverageApplied map that let hands race on this account-level,
	// single-value exchange setting.
	leverageSet map[string]bool

	// syncScheduled is 1 while a debounced post-order REST sync is pending.
	// Set by MarkSyncDirty (after fills); coalesces a fill burst into a single sync.
	syncScheduled atomic.Int32

	// ── Durability ───────────────────────────────────────────────────────────
	// nil fields degrade gracefully: poslog events are lost, events go to slog only.
	PosLog       poslog.Log                      // JetStream WAL for position events
	TradeLog     perf.TradeLog                   // JetStream HELM_TRADES — closed round-trip trades; TradePersister drains into PG
	PnLSummer    signalfollower.HandPnLSummer    // postgres aggregate query for RestorePnL + RestoreGuard; nil = degrade gracefully
	EventCounter signalfollower.HandEventCounter // postgres event-count aggregate for RestoreCounters; nil = counters start at 0
	SyncStore    SyncStore                       // persists last_synced_at after each successful portfolio sync; exported since Registry (package fleet) wires it in
	nc           *nats.Conn                      // NATS connection; used for portfolio.synced.* (nc.Publish path)
	js           nats.JetStreamContext           // JetStream context; publishes helm.events.* (durable, 7d)

	// ── WS fill stream lifecycle ─────────────────────────────────────────────
	// fillStreamCancel cancels the per-runtime WS stream context so RotateCreds
	// can disconnect and reconnect with new credentials without touching hands.
	// fillDrainCancel cancels the appCtx used by runFillProcessor / runLifecycleProcessor
	// (set only when StartStreaming starts the drain goroutines).
	fillStreamMu     sync.Mutex
	fillStreamCancel context.CancelFunc
	fillDrainCancel  context.CancelFunc

	// errStreaks counts consecutive PlaceOrder failures per exchange.ErrClass.
	// Reset to 0 (all classes) on any successful PlaceOrder. TriggerAccountError
	// fires only once a class's streak crosses its threshold in errClassThresholds
	// (see account_events.go) — tolerates transient blips (a clock-skew 401, one
	// dropped connection) without escalating on every single error.
	errStreaks [exchange.ErrClassCount]atomic.Int32

	// lastPermissions is the AccountPermissions from the previous successful
	// Sync(), used to detect a trading-capability flag flipping off between
	// syncs (see helm_sync.go). nil until the first sync with permission data.
	lastPermissions *exchange.AccountPermissions

	// AccountEvents is the outbound port for account/connection-level
	// conditions that warrant pausing/notifying the owning broker layer —
	// credential rejection, sustained network/exchange errors, margin calls,
	// trading restrictions. Set by the registry at spawn time via
	// Registry.SetAccountEventHandler. See account_events.go.
	AccountEvents AccountEventHandler

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
		hands:           make(map[string]*handEntry),
		router:          newOrderRouter(),
		MarketData:      market.NewExchangeData(), // default; overwritten by Registry.Spawn with shared bucket
		dedup:           newFillDedup(),
		resetTicker:     time.NewTicker(1 * time.Minute),
		stopCh:          make(chan struct{}),
		tradeReqCh:      make(chan *tradeRequest),
		fillReportCh:    make(chan helmdomain.FillReport),
		leverageReqCh:   make(chan *leverageRequest),
		feeEventCh:      make(chan FeeEvent),
		leverageSet:     make(map[string]bool),
	}
	if lastSyncedAt != nil {
		rt.lastSyncAtNano.Store(lastSyncedAt.UnixNano())
	}
	// Trade actor: the sole owner of Portfolio mutation + leverage state (see
	// helm_actor.go). Started unconditionally (not gated on StartStreaming/WS
	// availability) — many callers (tests, ProcessTrade/ReportFill from hands)
	// depend on it regardless of whether this exchange streams fills over WS.
	go rt.runTradeActor(context.Background())
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
func (r *HelmRuntime) ReconcileHand(ctx context.Context, hand *signalfollower.Hand) {
	if r.PosLog == nil {
		return
	}
	rec := signalfollower.NewReconciler(r.PosLog)
	rec.ReconcileSingle(ctx, r, hand)
}

// ── HelmPort accessors ──────────────────────────────────────────────────────
// Getter methods for the HelmPort interface: the struct fields below are
// already exported and used directly by other files in package actor, so they
// keep their names — these wrappers exist only so a Hand depending on the
// HelmPort interface (rather than the concrete struct) can reach them.

func (r *HelmRuntime) GetHelmID() uuid.UUID                             { return r.HelmID }
func (r *HelmRuntime) GetPortfolio() *portfolio.Portfolio               { return r.Portfolio }
func (r *HelmRuntime) GetExchange() exchange.Exchange                   { return r.Exchange }
func (r *HelmRuntime) GetCreds() exchange.Credentials                   { return r.Creds }
func (r *HelmRuntime) GetAccountID() uuid.UUID                          { return r.AccountID }
func (r *HelmRuntime) GetUserID() uuid.UUID                             { return r.UserID }
func (r *HelmRuntime) GetPosLog() poslog.Log                            { return r.PosLog }
func (r *HelmRuntime) GetTradeLog() perf.TradeLog                       { return r.TradeLog }
func (r *HelmRuntime) GetPnLSummer() signalfollower.HandPnLSummer       { return r.PnLSummer }
func (r *HelmRuntime) GetEventCounter() signalfollower.HandEventCounter { return r.EventCounter }

// Hands returns a snapshot of all hands currently registered with this
// runtime. Used by the reconciler (package signalfollower) instead of
// reaching into HelmRuntime's private hand registry directly.
func (r *HelmRuntime) Hands() []*signalfollower.Hand {
	r.mu.RLock()
	defer r.mu.RUnlock()
	hands := make([]*signalfollower.Hand, 0, len(r.hands))
	for _, e := range r.hands {
		hands = append(hands, e.h)
	}
	return hands
}

// PublishTradeFill publishes a trade.filled event when JetStream is available
// and reports whether it did — callers use the result to gate a paired
// MarkOrderFillPublished so a fill is never marked published without actually
// having been published (dev/test mode runs with nc/js nil).
func (r *HelmRuntime) PublishTradeFill(msg natsapi.TransactionMsg) bool {
	if r.js == nil {
		return false
	}
	natsapi.PublishTradeFill(r.js, msg)
	return true
}

// PublishSignal publishes a signal audit event when JetStream is available;
// a no-op otherwise (dev/test mode runs with nc/js nil).
func (r *HelmRuntime) PublishSignal(msg natsapi.SignalMsg) {
	if r.js == nil {
		return
	}
	natsapi.PublishSignal(r.js, msg)
}
