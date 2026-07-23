package signalfollower

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/core/tactics"
	"mallow/helm/internal/fleet/perf"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/infra/natsapi"
	handdomain "mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
)

// HandPnLSummer queries aggregate PnL metrics for a hand from PostgreSQL in one query.
// Implemented by tradelog.Log; injected into HelmRuntime to avoid draining JetStream
// history on every startup.
type HandPnLSummer interface {
	SumHandPnL(ctx context.Context, handID uuid.UUID) (totalPnL, totalCommission decimal.Decimal, wins, losses int64, err error)
	// RecentClosedPnL returns the PnL of a hand's last `limit` closed trades,
	// oldest first. Used by RestoreGuard to rebuild the edge-risk guard's ring
	// buffer on startup instead of replaying poslog history.
	RecentClosedPnL(ctx context.Context, handID uuid.UUID, limit int) ([]decimal.Decimal, error)
}

// HandEventCounter returns a hand's activity-event counts grouped by code, in one
// query. Implemented by eventlog.Log; used to rebuild signal/order counters on
// restart (PnL/win/loss come from HandPnLSummer).
type HandEventCounter interface {
	CountHandEvents(ctx context.Context, handID uuid.UUID) (map[int]int64, error)
}

// TradeProposal is a hand's request for account-level trade validation.
type TradeProposal struct {
	HandID string
	Symbol string
	Intent strategy.Intent
	Price  decimal.Decimal // optional: resolved from price cache when zero
	ATR    decimal.Decimal
	// EquityOverride, when positive, replaces portfolio equity for tactician sizing.
	// Hands with AllocatedCapital pass their realized equity (allocated + cumPnL)
	// so position sizes compound with the hand's actual performance.
	EquityOverride decimal.Decimal
	// AvailableBudget, when positive, is the hand's hard cap on per-entry notional —
	// the tactician clamps qty so qty*price ≤ AvailableBudget. Zero disables the cap.
	// Set only for allocated hands; shared-pool hands rely on helm-level risk guards.
	AvailableBudget decimal.Decimal
	// PositionQty is the per-hand qty from poslog (h.pos.ActiveLegs), not net portfolio.
	// Used by the tactician to size exits and scale-outs correctly when multiple hands
	// share the same symbol on this helm.
	PositionQty decimal.Decimal
}

// HelmPort is the capability surface HelmRuntime exposes to a Hand. *HelmRuntime
// (package actor) implements it; Hand depends on this interface, not the concrete
// struct, so package actor can own HelmRuntime while package signalfollower owns
// Hand without an import cycle: signalfollower never imports actor.
type HelmPort interface {
	// Trade actor entrypoints (see helm_actor.go).
	ProcessTrade(ctx context.Context, proposal TradeProposal, tact tactics.Planner) helmdomain.TradeReply
	ReportFill(fill helmdomain.FillReport)
	EnsureLeverage(ctx context.Context, symbol string, futures *handdomain.FuturesConfig)

	// Order routing (helm_orders.go).
	TrackOrder(orderID, handID string)
	RemoveOrderTracking(orderID string)
	MarkOrderFillPublished(orderID string)
	HasOrderFillPublished(orderID string) bool

	// Errors / account state.
	NoteOrderError(err error)
	ResetErrStreaks()
	IsPaused() bool

	// Events / NATS publish — wraps the JetStream-availability nil-check
	// internally so Hand never touches the raw *nats.Conn/JetStreamContext.
	EmitEvent(ev natsapi.HelmEvent)
	PublishTradeFill(msg natsapi.TransactionMsg) bool
	PublishSignal(msg natsapi.SignalMsg)

	// Market access.
	FiltersFor(ctx context.Context, symbol string) exchange.SymbolFilters
	LastKnownPrice(symbol string) decimal.Decimal
	NormalizeCommission(ctx context.Context, symbol string, side exchange.OrderSide, qty, price, commission decimal.Decimal, asset string) (decimal.Decimal, decimal.Decimal)

	// Reconciliation.
	ReconcileHand(ctx context.Context, hand *Hand)
	// Hands returns a snapshot of all hands currently registered with this
	// runtime — used by the reconciler instead of reaching into HelmRuntime's
	// private hand registry directly.
	Hands() []*Hand

	// Shared account-level resources.
	GetHelmID() uuid.UUID
	GetPortfolio() *portfolio.Portfolio
	GetExchange() exchange.Exchange
	GetCreds() exchange.Credentials
	GetAccountID() uuid.UUID
	GetUserID() uuid.UUID

	// Durability.
	GetPosLog() poslog.Log
	GetTradeLog() perf.TradeLog
	GetPnLSummer() HandPnLSummer
	GetEventCounter() HandEventCounter
}
