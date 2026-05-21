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

// TimeInForce controls how long an order remains active.
type TimeInForce string

const (
	TIFDefault TimeInForce = ""    // use exchange default (market=IOC, limit=GTC typically)
	TIFDay     TimeInForce = "day" // cancel at end of trading session
	TIFGTC     TimeInForce = "gtc" // good till cancelled
	TIFIOC     TimeInForce = "ioc" // immediate-or-cancel: fill available, cancel remainder
	TIFFOK     TimeInForce = "fok" // fill-or-kill: fill entire qty or reject
)

// AccountType mirrors the investment service's account type enum.
// Duplicated here to avoid import cycle across service boundaries.
type AccountType string

const (
	AccountSpot         AccountType = "spot"          // spot / equities (Alpaca, Binance spot)
	AccountFuturesUSDM  AccountType = "futures_usdm"  // USDT-margined perp/futures (Binance /fapi/)
	AccountFuturesCOINM AccountType = "futures_coinm" // coin-margined futures (Binance /dapi/)
	AccountUnified      AccountType = "unified"       // unified margin pool (OKX UTA, Bybit UTA)
	AccountOptions      AccountType = "options"       // options-only account
)

// Credentials holds per-account API credentials passed to each exchange call.
// The exchange client itself is stateless — one shared instance serves all accounts.
type Credentials struct {
	APIKey      string
	APISecret   string
	Passphrase  string      // OKX only
	AccountID   string      // OANDA only
	AccountType AccountType // spot | futures | unified
}

// OrderRequest contains parameters for placing an entry or exit order.
// SL/TP are NOT set here — use ExitOrderPlacer.PlaceExitOrders after fill.
type OrderRequest struct {
	Symbol     string
	Market     MarketKind // routes to spot or futures endpoint
	Side       OrderSide
	Type       OrderType
	TIF        TimeInForce     // time-in-force; zero = exchange default
	Qty        decimal.Decimal // base asset quantity; zero when QuoteQty is set
	QuoteQty   decimal.Decimal // quote asset quantity (e.g. 1000 USDT); spot market buy only; mutually exclusive with Qty
	Price      decimal.Decimal // only for limit orders
	ReduceOnly bool            // futures only: close-only, never opens a position
}

// ExitOrderRequest carries bracket order parameters for post-fill SL/TP placement.
// All prices are absolute. Zero value means not set.
type ExitOrderRequest struct {
	Symbol     string
	Market     MarketKind
	Side       OrderSide // exit side (opposite of entry)
	Qty        decimal.Decimal
	StopLoss   decimal.Decimal // absolute trigger price; zero = not set
	TakeProfit decimal.Decimal // absolute trigger price; zero = not set
}

// ExitOrderResult holds the exchange-assigned order IDs for placed exit orders.
// May contain 1 ID (OCO) or 2 IDs (STOP + TP as separate futures orders).
type ExitOrderResult struct {
	OrderIDs []string
}

// ExitOrderPlacer is an optional interface for exchanges that support
// post-fill bracket orders (SL/TP). Adapters that implement this will
// have PlaceExitOrders called by the hand after each entry fill.
//
// Binance spot: OCO order.
// Binance futures: separate STOP_MARKET + TAKE_PROFIT_MARKET orders.
// OKX: order-algo (oco or conditional).
type ExitOrderPlacer interface {
	PlaceExitOrders(ctx context.Context, creds Credentials, req ExitOrderRequest) (*ExitOrderResult, error)
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

// LeverageSetter is an optional interface for futures exchanges that support
// setting leverage and margin type per symbol.
// Implemented by adapters that support futures trading.
type LeverageSetter interface {
	// SetLeverage sets the leverage multiplier for a given symbol.
	// marginType: "isolated" | "cross". Called once before the first order.
	SetLeverage(ctx context.Context, creds Credentials, symbol string, leverage int, marginType string) error
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

// Backoff manages reconnection intervals with exponential backoff and jitter.
type Backoff struct {
	Min    time.Duration
	Max    time.Duration
	Factor float64
	Jitter bool
}

// Next calculates the next sleep duration given the current attempt number (0-indexed).
func (b *Backoff) Next(attempt int) time.Duration {
	min := b.Min
	if min <= 0 {
		min = 1 * time.Second
	}
	max := b.Max
	if max <= 0 {
		max = 30 * time.Second
	}
	factor := b.Factor
	if factor <= 0 {
		factor = 2.0
	}

	// Calculate base backoff: min * factor^attempt
	dur := float64(min)
	for i := 0; i < attempt; i++ {
		dur *= factor
		if dur >= float64(max) {
			dur = float64(max)
			break
		}
	}

	result := time.Duration(dur)
	if result > max {
		result = max
	}

	if b.Jitter {
		// Apply simple 10% random jitter: result * [0.95, 1.05]
		ns := time.Now().UnixNano()
		jitterRange := float64(result) * 0.1
		offset := float64(ns%1000) / 1000.0 * jitterRange
		result = result - time.Duration(jitterRange/2) + time.Duration(offset)
		if result < min {
			result = min
		}
		if result > max {
			result = max
		}
	}

	return result
}
