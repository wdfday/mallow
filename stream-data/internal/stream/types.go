package stream

import (
	"context"

	"stream-data/internal/model"
)

// Provider (TickProvider) streams raw ticks — used by TwelveData and any future
// tick-level sources. Ticks flow through BarAggregator before hitting JetStream.
type Provider interface {
	Name() string
	Stream(ctx context.Context, symbols []string) (<-chan model.Tick, error)
}

// BarProvider streams completed 1-minute OHLCV bars directly from the exchange
// (Binance @kline_1m, OKX candle1m, Alpaca bars). Bars go straight to JetStream,
// bypassing BarAggregator.
type BarProvider interface {
	Name() string
	StreamBars(ctx context.Context, symbols []string) (<-chan model.Bar, error)
}

// TickSink consumes a channel of ticks.
type TickSink interface {
	Run(ctx context.Context, ch <-chan model.Tick)
}

// Registration pairs a tick Provider with its symbols.
type Registration struct {
	Provider Provider
	Symbols  []string
}

// BarRegistration pairs a BarProvider with its symbols.
type BarRegistration struct {
	Provider BarProvider
	Symbols  []string
}
