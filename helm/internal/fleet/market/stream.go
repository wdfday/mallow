package market

import (
	"context"
	"log/slog"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/safe"

	"github.com/shopspring/decimal"
)

// tickHandler receives a bare-symbol last-trade price.
type tickHandler func(symbol string, price decimal.Decimal)

// bookHandler receives a top-5 L2 snapshot for a bare symbol.
type bookHandler func(symbol string, snap exchange.L2Snapshot)

// exchangeStreamer is a single-WebSocket public data feed for one exchange:
// one connection, carrying both the trade-price channel and the L2 book
// channel for every subscribed symbol. Blocks until ctx is cancelled,
// reconnecting internally on failure.
type exchangeStreamer func(ctx context.Context, symbols []string, onTick tickHandler, onBook bookHandler) error

// streamers maps exchange name (exchange.Exchange.Name()) to its public,
// no-credential WebSocket streamer. Exchanges not listed here (e.g. alpaca,
// fbinance) have no public WS coverage yet — StartStreaming logs a warning
// and skips them rather than silently pretending to cover them.
var streamers = map[string]exchangeStreamer{
	"binance": streamBinance,
	"okx":     streamOKX,
	"bybit":   streamBybit,
}

// StartStreaming self-connects to every exchange present in symbolsByExchange
// and becomes the sole writer of that exchange's price + L2 book data in this
// MarketContext. Exactly one WebSocket connection is opened per exchange, regardless
// of how many helms/hands trade on it — this method is idempotent per
// exchange, so calling it again (e.g. once more exchanges have hydrated
// hands) only starts streamers for exchanges not already running.
//
// This has no dependency on herald/NATS — it dials each exchange's public
// WebSocket directly.
func (c *MarketContext) StartStreaming(ctx context.Context, symbolsByExchange map[exchange.Exchange][]string) {
	for ex, symbols := range symbolsByExchange {
		if len(symbols) == 0 {
			continue
		}
		name := ex.Name()
		streamer, ok := streamers[name]
		if !ok {
			slog.Warn("market streaming: no public WS listener for exchange, skipping",
				"exchange", name, "symbols", symbols)
			continue
		}
		if !c.markStreaming(name) {
			continue // already running (or starting) for this exchange
		}
		go c.runStreamer(ctx, name, streamer, symbols)
	}
}

func (c *MarketContext) runStreamer(ctx context.Context, exName string, streamer exchangeStreamer, symbols []string) {
	defer safe.Recover()
	slog.Info("market streaming: starting", "exchange", exName, "symbols", symbols)
	onTick := func(symbol string, price decimal.Decimal) {
		c.UpdatePrice(exName, symbol, price)
	}
	onBook := func(symbol string, snap exchange.L2Snapshot) {
		c.UpdateBook(exName, symbol, snap)
	}
	if err := streamer(ctx, symbols, onTick, onBook); err != nil {
		slog.Error("market streaming: exited with error", "exchange", exName, "err", err)
	}
}
