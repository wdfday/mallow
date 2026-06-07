package exchange

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// Per-surface order DTOs.
//
// An order is observed across four exchange surfaces, each with a deliberately distinct
// shape — they are NOT merged into one fat type, because each carries a different field
// subset and a union would be mostly-optional and lossy:
//
//	OrderResult         — REST PlaceOrder / GetOrder result   (status, filled qty/avg, commission)
//	AccountTransaction  — REST sync / trade-history fill       (trade id, fee, filled_at)
//	WsFillEvent         — private WS fill push                 (incremental qty, partial flag)
//	OrderLifecycleEvent — private WS ack / cancel push         (no fill data)
//
// INVARIANT: all four carry the order-identity pair { exchange order id, ClientOrderID }.
// When adding or changing an identity-related field, change it on ALL FOUR so routing and
// attribution stay consistent. The ClidSurfaces contract (see ClidCapable) declares which
// of these surfaces actually echo the clid per venue. See CLIENT_ORDER_ID.md.

// ── Account sync ──────────────────────────────────────────────────────────────

// ExchangePosition is a position as reported by the exchange REST API.
type ExchangePosition struct {
	Symbol   string
	Qty      decimal.Decimal // positive = long, negative = short (futures)
	AvgPrice decimal.Decimal
	CurPrice decimal.Decimal
}

// AssetBalance is a single asset's free balance as returned by the exchange.
type AssetBalance struct {
	Asset string
	Free  decimal.Decimal
}

// AccountTransaction is a single filled order as returned by the exchange REST API.
type AccountTransaction struct {
	TradeID string // exchange fill/trade ID — dedup key; empty if exchange doesn't provide one
	OrderID string
	// ClientOrderID is the caller-supplied clOrdId echoed by the exchange, when available.
	// Lets the REST-sync path attribute a fill to its hand via the clid (race-free key),
	// the same as the WS path. Empty when the exchange's trade record omits it.
	ClientOrderID string
	Symbol        string
	Side          string // "buy" | "sell"
	Qty           decimal.Decimal
	AvgPrice      decimal.Decimal
	Fee           decimal.Decimal
	FeeAsset      string // asset the fee was charged in (e.g. "ETH", "USDT"); empty = unknown
	FilledAt      time.Time
}

// AccountSnapshot is the current account state fetched from the exchange REST API.
type AccountSnapshot struct {
	Cash         decimal.Decimal
	Equity       decimal.Decimal
	Positions    []ExchangePosition
	Transactions []AccountTransaction // recent filled orders since last sync
	Balances     []AssetBalance       // per-asset free balances
}

// AccountSyncer is optionally implemented by exchanges that support polling
// account state via REST. since, if non-nil, requests only transactions after that time.
type AccountSyncer interface {
	SyncAccount(ctx context.Context, creds Credentials, since *time.Time) (*AccountSnapshot, error)
}

// ── Balance event streaming ───────────────────────────────────────────────────

// BalanceEvent is pushed by the exchange private WS whenever an asset's free
// balance changes — on fills, deposits, withdrawals, or fee deductions.
type BalanceEvent struct {
	Asset string
	Free  decimal.Decimal
	At    time.Time
}

// ── Position event streaming ──────────────────────────────────────────────────

// PositionSide indicates the direction of a futures position.
type PositionSide string

const (
	PositionLong  PositionSide = "long"
	PositionShort PositionSide = "short"
	PositionNet   PositionSide = "net" // one-way / both mode (Binance BOTH, OKX net)
)

// PositionEvent is pushed by the exchange private WS whenever a futures position changes.
// Emitted on order fill that opens/closes/changes a position, and on account sync events.
// For spot-only accounts this event is never emitted — positions are implicit from holdings.
type PositionEvent struct {
	Symbol string
	Side   PositionSide
	// Size is signed: positive = long, negative = short, zero = position closed/flat.
	Size          decimal.Decimal
	EntryPrice    decimal.Decimal
	UnrealizedPnL decimal.Decimal
	At            time.Time
}

// RiskEvent is pushed by the exchange when account margin risk changes —
// typically a margin-call warning or when margin ratio crosses a threshold.
// Symbol is empty for account-level risk events; non-empty for per-position risk.
type RiskEvent struct {
	Symbol           string
	MarginRatio      decimal.Decimal // maintenance margin ratio; approaches 1.0 near liquidation
	LiquidationPrice decimal.Decimal // estimated liq price; zero if not provided
	At               time.Time
}

// ── Order lifecycle event streaming ──────────────────────────────────────────

// OrderLifecycleEventType classifies a private WS order lifecycle event (not a fill).
type OrderLifecycleEventType string

const (
	// OrderLifecycleEventLive is emitted when an order is acknowledged by the exchange (NEW/accepted).
	OrderLifecycleEventLive OrderLifecycleEventType = "live"
	// OrderLifecycleEventCanceled is emitted when an order is canceled, rejected, or expired.
	OrderLifecycleEventCanceled OrderLifecycleEventType = "canceled"
)

// OrderLifecycleEvent is received from the exchange private WebSocket when an
// order is acknowledged (live) or canceled/rejected. It carries no fill data.
// See WsFillEvent for fill notifications.
type OrderLifecycleEvent struct {
	Type OrderLifecycleEventType
	// OrderID is the exchange-assigned id.
	OrderID string
	// ClientOrderID is the caller-supplied clOrdId echoed back by the exchange.
	// Empty for orders placed without one (manual orders, bracket orders, legacy path).
	ClientOrderID string
	Symbol        string
	Side          OrderSide
	Qty           decimal.Decimal // original submitted qty
	Timestamp     time.Time
}

// ── Fill event streaming ──────────────────────────────────────────────────────

// WsFillEvent is a single fill notification from the exchange private WebSocket.
// FilledQty is ALWAYS incremental (this-fill qty only), never cumulative.
// Adapters that receive cumulative qty from the exchange (e.g. Alpaca) compute
// the delta internally before emitting this event — callers may rely on the
// incremental contract without additional bookkeeping.
type WsFillEvent struct {
	OrderID string
	// ClientOrderID is the caller-supplied clOrdId echoed back by the exchange.
	// When non-empty it is the canonical routing key (set before the order was placed,
	// so it is always known by the time the fill arrives). Empty for bracket/manual
	// orders and adapters that do not yet map it — those fall back to OrderID routing.
	ClientOrderID   string
	TradeID         string // exchange fill ID; unique per partial fill
	Symbol          string
	Side            OrderSide
	Partial         bool            // true = partial fill; false = fully filled
	FilledQty       decimal.Decimal // INCREMENTAL: qty filled in this specific fill event only
	FilledAvg       decimal.Decimal // avg price of this fill event
	Commission      decimal.Decimal // fee charged; zero when not provided by exchange
	CommissionAsset string          // e.g. "ETH", "BNB", "USDT"
	Timestamp       time.Time
}

// ClidSurfaces declares which exchange surfaces echo the caller-supplied client order id
// (clOrdId) back to us. Routing and REST-sync attribution use the clid wherever it is
// echoed; a surface that doesn't echo falls back to the exchange order id.
//
// Every adapter that sends a clid in PlaceOrder MUST declare its echo surfaces (via
// ClidCapable) so a missing one is visible in code + caught by the conformance test,
// rather than degrading to a silent attribution gap. WS and OrderQuery are REQUIRED for
// clid routing to function at all; TradeHistory is optional (e.g. Binance's spot trade
// list omits clOrdId, so REST-sync attribution there falls back to the exchange id).
// See CLIENT_ORDER_ID.md.
type ClidSurfaces struct {
	WS           bool // private WS order/fill stream echoes clid
	OrderQuery   bool // GetOrder / GetOrderByClientOrderID / ListOpenOrders echo clid
	TradeHistory bool // REST sync / trade-history echoes clid
}

// ClidCapable is implemented by adapters that support client order ids. It pairs with
// ClientOrderQuerier: an adapter that can query by clid must also declare where the clid
// is echoed. ClidSurfaces must not deref adapter state (callable on a nil receiver) so the
// conformance test can inspect the declaration without live credentials.
type ClidCapable interface {
	ClidSurfaces() ClidSurfaces
}

// ClientOrderQuerier is optionally implemented by exchanges that can look up an order
// by the caller-supplied client order id (clOrdId). Used to recover from an ambiguous
// PlaceOrder failure (timeout / network drop) — query whether the order actually landed
// instead of blindly assuming it did not. See CLIENT_ORDER_ID.md.
type ClientOrderQuerier interface {
	// GetOrderByClientOrderID returns the order matching clientOrderID, or nil when the
	// exchange has no such order (or the lookup itself fails — callers treat nil as
	// "could not confirm"). symbol is required by venues that key lookups by instrument
	// (Binance, OKX); market selects the spot vs futures endpoint/category for venues
	// that segregate them (Binance, Bybit). Alpaca ignores both.
	GetOrderByClientOrderID(ctx context.Context, creds Credentials, symbol string, market MarketKind, clientOrderID string) (*OrderResult, error)
}

// AccountStreamer is optionally implemented by exchanges that support private
// WebSocket streaming for account order and fill events.
//
//   - onLifecycle is called for order ack (live) and cancel/reject events.
//     May be nil if the caller only cares about fills.
//   - onFill is called for each fill event (partial or fully filled).
//     WsFillEvent.FilledQty is always incremental — adapters handle the delta internally.
//     May be nil if the caller only cares about lifecycle events.
//   - onBalance is called on balance-change events (deposits, withdrawals, fee deductions).
//     May be nil; exchanges that do not push balance events on this connection ignore it.
//   - onPosition is called on futures position updates (size, entry price, unrealized PnL).
//     May be nil; spot-only exchanges never call it.
//   - onRisk is called on margin-call or liquidation-warning events.
//     May be nil; spot-only exchanges never call it.
type AccountStreamer interface {
	StreamOrders(
		ctx context.Context,
		creds Credentials,
		onLifecycle func(OrderLifecycleEvent),
		onFill func(WsFillEvent),
		onBalance func(BalanceEvent),
		onPosition func(PositionEvent), // futures position update; nil = ignore
		onRisk func(RiskEvent), // margin-call / liquidation warning; nil = ignore
	) error
}

// ── Legacy fill channel streaming ────────────────────────────────────────────

// FillStreamer is optionally implemented by exchanges that expose a fill-event
// channel via SubscribeFills. It is NOT used by the live trading runtime —
// AccountStreamer.StreamOrders is the canonical live path.
//
// FillStreamer is retained for integration tests and tooling that need a simple
// channel-based fill source without the full two-callback StreamOrders API.
// Each adapter implements it by bridging over StreamOrders internally.
type FillStreamer interface {
	// SubscribeFills opens a WebSocket subscription to fill events for the account.
	// The returned channel is closed when ctx is cancelled.
	// Callers should NOT call both SubscribeFills and StartFillStreaming on the same
	// account — that opens two parallel WS connections and produces duplicate events.
	SubscribeFills(ctx context.Context, creds Credentials) (<-chan FillEvent, error)
}

// ── Price fetch ───────────────────────────────────────────────────────────────

// PriceFetcher is optionally implemented by exchanges that support on-demand
// REST price lookup. Used as a cache-miss fallback in ProcessTrade.
type PriceFetcher interface {
	GetCurrentPrice(ctx context.Context, creds Credentials, symbol string) (decimal.Decimal, error)
}

// ── Spot balance fetch ────────────────────────────────────────────────────────

// SpotBalanceFetcher is optionally implemented by spot exchanges.
// Used as a fallback when a SELL exit fails with insufficient balance:
// query the actual free balance of the base asset and retry with that qty.
type SpotBalanceFetcher interface {
	GetFreeBalance(ctx context.Context, creds Credentials, asset string) (decimal.Decimal, error)
}

// ── Market data streaming ─────────────────────────────────────────────────────

// MarketStreamer is a shared, broker-level WebSocket client for live market data.
// One instance per broker type, shared across all helms of that broker.
type MarketStreamer interface {
	// Subscribe streams live prices for the given symbols until ctx is canceled.
	Subscribe(ctx context.Context, symbols []string) error
	// AddPriceHandler registers a callback fired on each live trade price.
	AddPriceHandler(h func(symbol string, price decimal.Decimal))
}

// ── L2 order book streaming ───────────────────────────────────────────────────

// L2Level is one price level in a top-of-book snapshot.
type L2Level struct {
	Price decimal.Decimal
	Size  decimal.Decimal
}

// L2Snapshot is a top-5 bid/ask snapshot from the books5 channel.
// Timestamp is set at dispatch time (Go side), not from the exchange payload.
type L2Snapshot struct {
	Symbol    string
	Timestamp time.Time
	Bids      [5]L2Level // descending: best bid first
	Asks      [5]L2Level // ascending: best ask first
}

// BookStreamer is optionally implemented by market streamers that publish
// L2 order book snapshots. Currently: OKX via the books5 channel.
type BookStreamer interface {
	AddBookHandler(h func(L2Snapshot))
}

// ── Order reconciliation ──────────────────────────────────────────────────────

// OrderReconciler is optionally implemented by exchanges that can list open
// orders via REST. Used on startup to rebuild the in-memory orderbook after a crash.
type OrderReconciler interface {
	GetPendingOrders(ctx context.Context, creds Credentials, symbol string) ([]OrderResult, error)
}

// HistoryFetcher is optionally implemented by exchanges that support querying
// filled order history over a time range. Used for gap recovery on restart:
// replays fills that occurred while helm was offline so investment JetStream
// stays consistent.
//
// symbols is a hint for exchanges that require a per-symbol query (e.g. Binance).
// Exchanges that support global history queries (OKX, Bybit, Alpaca) may ignore it.
// Pass the set of symbols currently in the orderbook / poslog as the hint.
type HistoryFetcher interface {
	FilledOrders(ctx context.Context, creds Credentials, symbols []string, from, to time.Time) ([]AccountTransaction, error)
}

// IsolatedMarginTrader is implemented by futures exchanges that support per-symbol
// isolated margin. Exchanges that do NOT implement this interface are assumed to
// support cross margin only — hand creation with MarginType="isolated" is blocked.
type IsolatedMarginTrader interface {
	SupportsIsolatedMargin() bool
}

// LiveAlgoOrder is a summary of an active exchange-side algo (OCO/conditional) order.
type LiveAlgoOrder struct {
	AlgoID     string
	InstID     string
	OrdType    string // "oco" | "conditional"
	StopLoss   decimal.Decimal
	TakeProfit decimal.Decimal
}

// AlgoOrderLister is optionally implemented by exchanges that support algo/bracket
// orders (OKX OCO/conditional). Used during startup recovery to find existing live
// algo orders for a symbol — prevents duplicate OCO placement when KindBracketPlaced
// was not persisted before a crash.
type AlgoOrderLister interface {
	ListLiveAlgoOrders(ctx context.Context, creds Credentials, instID string) ([]LiveAlgoOrder, error)
}
