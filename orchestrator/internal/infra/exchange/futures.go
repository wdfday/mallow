package exchange

import (
	"context"

	"github.com/shopspring/decimal"
)

// FuturesTrader is implemented by exchanges that support futures/perpetual trading.
// Binance futures, OKX swap, Bybit perpetual all implement this.
// Alpaca implements it but returns SupportsFutures() = false.
type FuturesTrader interface {
	// SupportsFutures returns false for spot-only exchanges (e.g. Alpaca).
	SupportsFutures() bool
	// SetLeverage configures leverage and margin type for a symbol.
	// Must be called before placing the first futures order on that symbol.
	SetLeverage(ctx context.Context, symbol string, leverage int, marginType string) error
	// FundingRate returns the current funding rate for a perpetual contract.
	FundingRate(ctx context.Context, symbol string) (decimal.Decimal, error)
	// MarkPrice returns the current mark price used for liquidation calculation.
	MarkPrice(ctx context.Context, symbol string) (decimal.Decimal, error)
}
