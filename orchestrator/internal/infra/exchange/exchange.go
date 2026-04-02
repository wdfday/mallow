package exchange

import "context"

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

// OrderRequest contains parameters for placing an order.
type OrderRequest struct {
	Symbol       string
	Market       MarketKind // routes to spot or futures endpoint
	Side         OrderSide
	Type         OrderType
	Qty          float64
	Price        float64 // only for limit orders
	StopLoss     float64 // optional: bracket/OTO fixed stop price
	TakeProfit   float64 // optional: bracket/OTO limit price
	TrailingStop float64 // optional: trailing stop as fraction of entry (e.g. 0.02 = 2%); mutually exclusive with StopLoss
	ReduceOnly   bool    // futures only: close-only, never opens a position
}

// OrderResult contains the result of an order operation.
type OrderResult struct {
	ID        string
	Symbol    string
	Side      OrderSide
	Status    string
	Qty       float64
	FilledQty float64
	FilledAvg float64
}

// Exchange is the core interface every broker adapter must implement.
// It covers order placement and status — the minimum needed by a trading bot.
type Exchange interface {
	// Name returns the exchange identifier (e.g. "alpaca", "binance").
	Name() string
	// PlaceOrder submits a new order. Market field routes to spot or futures endpoint.
	PlaceOrder(ctx context.Context, req OrderRequest) (*OrderResult, error)
	// GetOrder retrieves the current status of an order by ID.
	GetOrder(ctx context.Context, orderID string) (*OrderResult, error)
	// CancelOrder cancels a pending order by ID.
	CancelOrder(ctx context.Context, orderID string) error
}
