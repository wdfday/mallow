package runtime

import (
	"log/slog"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// UpdatePrice stores the latest market price for a symbol and forwards it to the portfolio.
func (r *HelmRuntime) UpdatePrice(symbol string, price decimal.Decimal) {
	if !price.IsPositive() {
		return
	}
	r.pricesMu.Lock()
	r.prices[symbol] = price
	r.pricesMu.Unlock()
	r.Portfolio.UpdatePrice(symbol, price)
}

func (r *HelmRuntime) lastKnownPrice(symbol string) decimal.Decimal {
	r.pricesMu.RLock()
	p := r.prices[symbol]
	r.pricesMu.RUnlock()
	if p.IsPositive() {
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
