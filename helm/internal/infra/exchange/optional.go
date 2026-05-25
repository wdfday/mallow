package exchange

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

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
	TradeID  string // exchange fill/trade ID — dedup key; empty if exchange doesn't provide one
	OrderID  string
	Symbol   string
	Side     string // "buy" | "sell"
	Qty      decimal.Decimal
	AvgPrice decimal.Decimal
	Fee      decimal.Decimal
	FilledAt time.Time
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

// ── Order event streaming ─────────────────────────────────────────────────────

// OrderEventType classifies a private WS order lifecycle event.
type OrderEventType string

const (
	OrderEventLive        OrderEventType = "live"         // order acknowledged by exchange
	OrderEventPartialFill OrderEventType = "partial_fill" // partial fill received
	OrderEventFilled      OrderEventType = "filled"       // fully filled
	OrderEventCanceled    OrderEventType = "canceled"     // canceled or rejected
)

// OrderEvent is received from the exchange private WebSocket on every order
// state change. FilledQty/FilledAvg are zero for live and canceled events.
type OrderEvent struct {
	Type    OrderEventType
	OrderID string
	// TradeID is the exchange-assigned fill ID for this specific fill event.
	// Unique per partial fill — used as the dedup key for investment transactions.
	// Empty for live and canceled events; set to orderID+"_open" / orderID+"_cancel" by the publisher.
	TradeID         string
	Symbol          string
	Side            OrderSide
	Qty             decimal.Decimal // original submitted qty; populated on live events
	FilledQty       decimal.Decimal // this-event fill qty; zero for live/canceled
	FilledAvg       decimal.Decimal // this-event fill price; zero for live/canceled
	Commission      decimal.Decimal // fee charged for this fill; zero when not provided by exchange
	CommissionAsset string          // asset the fee was taken from: "ETH", "BNB", "USDT", etc.
	Timestamp       time.Time
}

// AccountStreamer is optionally implemented by exchanges that support private
// WebSocket streaming for account order lifecycle events.
// balanceHandler is optional (nil = ignore balance events); exchanges that do
// not push balance updates on the same connection may ignore it entirely.
type AccountStreamer interface {
	StreamOrders(ctx context.Context, creds Credentials, orderHandler func(OrderEvent), balanceHandler func(BalanceEvent)) error
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
