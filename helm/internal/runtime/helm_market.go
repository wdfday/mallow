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
	stripped := stripExchangePrefix(symbol)
	r.pricesMu.Lock()
	r.prices[symbol] = price
	if stripped != symbol {
		r.prices[stripped] = price
	}
	r.pricesMu.Unlock()
	r.Portfolio.UpdatePrice(symbol, price)
}

func (r *HelmRuntime) lastKnownPrice(symbol string) decimal.Decimal {
	stripped := stripExchangePrefix(symbol)
	r.pricesMu.RLock()
	p := r.prices[symbol]
	if !p.IsPositive() && stripped != symbol {
		p = r.prices[stripped]
	}
	r.pricesMu.RUnlock()
	if p.IsPositive() {
		return p
	}
	// Fallback: L2 mid-price (bid[0]+ask[0])/2 — more accurate than last trade
	// when the trade price cache is cold.
	if snap, ok := r.getL2(symbol); ok {
		bid := snap.Bids[0].Price
		ask := snap.Asks[0].Price
		if bid.IsPositive() && ask.IsPositive() {
			return bid.Add(ask).Div(decimal.NewFromInt(2))
		}
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
	if r.getL2 == nil {
		return exchange.L2Snapshot{}, false
	}
	return r.getL2(symbol)
}

// EnqueueOrderEvent drops a broker order event into the runtime's channel non-blocking.
func (r *HelmRuntime) EnqueueOrderEvent(ev exchange.OrderEvent) {
	select {
	case r.orderCh <- ev:
	default:
		slog.Error("order channel full, dropping event",
			"helm_id", r.HelmID,
			"type", ev.Type,
			"order_id", ev.OrderID,
			"symbol", ev.Symbol,
		)
	}
}
