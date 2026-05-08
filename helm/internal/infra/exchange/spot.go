package exchange

import (
	"context"

	"github.com/shopspring/decimal"
)

// SpotTrader is implemented by exchanges that support spot (cash) trading.
// Alpaca, Binance spot, OKX spot all implement this.
type SpotTrader interface {
	// SpotBalance returns the available balance for the given asset (e.g. "USD", "BTC").
	SpotBalance(ctx context.Context, asset string) (decimal.Decimal, error)
}
