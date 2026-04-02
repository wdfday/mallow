package marketdata

import "context"

// Listener streams real-time price ticks from a market data source.
// Implementations are listen-only — they never place orders.
type Listener interface {
	// Name returns the source identifier (e.g. "alpaca", "okx").
	Name() string

	// Subscribe connects to the market data source and calls onTick for each
	// trade. Blocks until ctx is cancelled. Reconnects automatically on errors.
	Subscribe(ctx context.Context, symbols []string, onTick func(symbol string, price float64)) error
}
