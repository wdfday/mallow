package exchange

import (
	"context"
	"time"
)

// ── Account sync ──────────────────────────────────────────────────────────────

// ExchangePosition is a position as reported by the exchange REST API.
type ExchangePosition struct {
	Symbol   string
	Qty      float64 // positive = long, negative = short (futures)
	AvgPrice float64
	CurPrice float64
}

// AccountTransaction is a single filled order as returned by the exchange REST API.
type AccountTransaction struct {
	OrderID  string
	Symbol   string
	Side     string // "buy" | "sell"
	Qty      float64
	AvgPrice float64
	Fee      float64
	FilledAt time.Time
}

// AccountSnapshot is the current account state fetched from the exchange REST API.
type AccountSnapshot struct {
	Cash         float64
	Equity       float64
	Positions    []ExchangePosition
	Transactions []AccountTransaction // recent filled orders since last sync
}

// AccountSyncer is optionally implemented by exchanges that support polling
// account state via REST. since, if non-nil, requests only transactions after that time.
type AccountSyncer interface {
	SyncAccount(ctx context.Context, since *time.Time) (*AccountSnapshot, error)
}

// ── Fill streaming ────────────────────────────────────────────────────────────

// FillEvent is a completed (or partial) fill received from the exchange's
// private account WebSocket stream.
type FillEvent struct {
	OrderID   string
	Symbol    string
	Side      OrderSide
	FilledQty float64
	FilledAvg float64
	Timestamp time.Time
}

// AccountStreamer is optionally implemented by exchanges that support private
// WebSocket streaming for account fill events.
type AccountStreamer interface {
	StreamFills(ctx context.Context, handler func(FillEvent)) error
}

// ── Price fetch ───────────────────────────────────────────────────────────────

// PriceFetcher is optionally implemented by exchanges that support on-demand
// REST price lookup. Used as a cache-miss fallback in ProcessTrade.
type PriceFetcher interface {
	GetCurrentPrice(ctx context.Context, symbol string) (float64, error)
}

// ── Market data streaming ─────────────────────────────────────────────────────

// MarketStreamer is a shared, broker-level WebSocket client for live market data.
// One instance per broker type, shared across all orchestrators of that broker.
type MarketStreamer interface {
	// Subscribe streams live prices for the given symbols until ctx is cancelled.
	Subscribe(ctx context.Context, symbols []string) error
	// AddPriceHandler registers a callback fired on each live trade price.
	AddPriceHandler(h func(symbol string, price float64))
}
