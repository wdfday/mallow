package runtime

import (
	"log/slog"
	"strings"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// stripExchangePrefix removes the "exchange:" prefix that herald attaches to
// symbol names (e.g. "binance:ETHUSDT" → "ETHUSDT", "okx:BTC-USDT" → "BTC-USDT").
// Symbols without a prefix are returned unchanged.
func stripExchangePrefix(symbol string) string {
	if _, after, found := strings.Cut(symbol, ":"); found {
		return after
	}
	return symbol
}

// UpdatePrice stores the latest market price for a symbol and forwards it to the portfolio.
// Stores under both the raw key and the prefix-stripped key so that lookups
// using either form ("ETHUSDT" or "binance:ETHUSDT") succeed.
func (r *HelmRuntime) UpdatePrice(symbol string, price decimal.Decimal) {
	if !price.IsPositive() {
		return
	}
	r.prices.set(symbol, price)
	r.Portfolio.UpdatePrice(symbol, price)
}

func (r *HelmRuntime) lastKnownPrice(symbol string) decimal.Decimal {
	// priceCache covers the trade cache + L2 mid-price; the Portfolio is the final fallback.
	if p := r.prices.get(symbol); p.IsPositive() {
		return p
	}
	if pos := r.Portfolio.GetPosition(symbol); pos != nil {
		return pos.CurrentPrice
	}
	return decimal.Zero
}

// LatestL2 returns the most recent L2 snapshot for a symbol.
// Delegates to the registry-level broker cache injected at Spawn().
// ok=false when no L2 streamer is connected or no snapshot has been received yet.
func (r *HelmRuntime) LatestL2(symbol string) (exchange.L2Snapshot, bool) {
	return r.prices.latestL2(symbol)
}

// EnqueueLifecycleEvent drops a broker order lifecycle event (ack/cancel) into
// the runtime's lifecycle channel, non-blocking (drops on full with an error log).
func (r *HelmRuntime) EnqueueLifecycleEvent(ev exchange.OrderLifecycleEvent) {
	select {
	case r.lifecycleCh <- ev:
	default:
		slog.Error("lifecycle channel full, dropping event",
			"helm_id", r.HelmID,
			"type", ev.Type,
			"order_id", ev.OrderID,
			"symbol", ev.Symbol,
		)
	}
}

// EnqueueWsFill drops a WS fill event into the runtime's fill channel, non-blocking.
// Drops on full — the REST poll fallback will catch the fill within 5s.
func (r *HelmRuntime) EnqueueWsFill(ev exchange.WsFillEvent) {
	select {
	case r.wsFillCh <- ev:
	default:
		slog.Error("ws fill channel full, dropping fill — REST poll will recover",
			"helm_id", r.HelmID,
			"order_id", ev.OrderID,
			"symbol", ev.Symbol,
			"qty", ev.FilledQty,
		)
	}
}
