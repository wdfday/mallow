package exchange

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// MarketKind identifies which market segment an order targets.
// The exchange adapter uses this to route to the correct endpoint.
type MarketKind string

const (
	MarketSpot    MarketKind = "spot"
	MarketFutures MarketKind = "futures"
)

// OrderSide represents the direction of a trade.
type OrderSide string

const (
	Buy  OrderSide = "buy"
	Sell OrderSide = "sell"
)

// OrderType represents the type of order.
type OrderType string

const (
	Market OrderType = "market"
	Limit  OrderType = "limit"
)

// Credentials holds per-account API credentials passed to each exchange call.
// The exchange client itself is stateless — one shared instance serves all accounts.
type Credentials struct {
	APIKey     string
	APISecret  string
	Passphrase string // OKX only
	AccountID  string // OANDA only
}

// OrderRequest contains parameters for placing an order.
type OrderRequest struct {
	Symbol       string
	Market       MarketKind // routes to spot or futures endpoint
	Side         OrderSide
	Type         OrderType
	Qty          decimal.Decimal
	Price        decimal.Decimal // only for limit orders
	StopLoss     decimal.Decimal // optional: bracket/OTO fixed stop price
	TakeProfit   decimal.Decimal // optional: bracket/OTO limit price
	TrailingStop decimal.Decimal // optional: trailing stop as fraction of entry (e.g. 0.02 = 2%); mutually exclusive with StopLoss
	ReduceOnly   bool            // futures only: close-only, never opens a position
}

// OrderResult contains the result of an order operation.
type OrderResult struct {
	ID        string
	Symbol    string
	Side      OrderSide
	Status    string
	Qty       decimal.Decimal
	FilledQty decimal.Decimal
	FilledAvg decimal.Decimal
}

// PositionResult is a position currently held at the exchange.
type PositionResult struct {
	Symbol    string
	Side      OrderSide
	Qty       decimal.Decimal
	AvgPrice  decimal.Decimal
	UnrealPnL decimal.Decimal
}

// FillEvent is a real-time fill notification pushed from the exchange WebSocket.
type FillEvent struct {
	OrderID   string
	Symbol    string
	Side      OrderSide
	FilledQty decimal.Decimal
	FillPrice decimal.Decimal
	Timestamp time.Time
}

// Exchange is the core interface every broker adapter must implement.
// Implementations are stateless — credentials are passed per call so one instance
// can serve multiple accounts without re-instantiation.
type Exchange interface {
	// Name returns the exchange identifier (e.g. "alpaca", "binance").
	Name() string

	// ── Order lifecycle ───────────────────────────────────────────────────────

	// PlaceOrder submits a new order. Market field routes to spot or futures endpoint.
	PlaceOrder(ctx context.Context, creds Credentials, req OrderRequest) (*OrderResult, error)
	// GetOrder retrieves the current status of an order by ID.
	GetOrder(ctx context.Context, creds Credentials, orderID string) (*OrderResult, error)
	// CancelOrder cancels a pending order by ID.
	CancelOrder(ctx context.Context, creds Credentials, orderID string) error

	// ── Reconciliation ────────────────────────────────────────────────────────

	// ListOpenOrders returns all orders that are not yet filled or cancelled.
	// Pass symbol="" to list across all symbols (exchange-dependent behavior).
	// Used by the reconciler on startup to cross-reference the poslog.
	ListOpenOrders(ctx context.Context, creds Credentials, symbol string) ([]OrderResult, error)

	// ListPositions returns all currently held positions for the account.
	// Used by the reconciler to confirm PhaseOpen state is still accurate.
	ListPositions(ctx context.Context, creds Credentials) ([]PositionResult, error)

	// ── Live fill stream ─────────────────────────────────────────────────────

	// SubscribeFills opens a WebSocket subscription to fill events for the account.
	// The returned channel is closed when ctx is cancelled or the connection drops.
	// Callers should reconnect on close. Fills arrive here before GetOrder polling.
	SubscribeFills(ctx context.Context, creds Credentials) (<-chan FillEvent, error)
}
