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

// UpdateL2 caches the latest books5 snapshot and pushes it to running hands
// watching that symbol. Called from the shared market streamer goroutine —
// must not block; OnL2 on each hand must be fast.
func (r *HelmRuntime) UpdateL2(snap exchange.L2Snapshot) {
	r.l2Mu.Lock()
	r.l2Books[snap.Symbol] = snap
	r.l2Mu.Unlock()

	r.mu.RLock()
	for _, hand := range r.bots {
		if hand.Symbol == snap.Symbol && hand.IsRunning() {
			hand.OnL2(snap)
		}
	}
	r.mu.RUnlock()
}

// LatestL2 returns the most recent books5 snapshot for a symbol.
// ok=false if no snapshot has been received yet.
func (r *HelmRuntime) LatestL2(symbol string) (exchange.L2Snapshot, bool) {
	r.l2Mu.RLock()
	s, ok := r.l2Books[symbol]
	r.l2Mu.RUnlock()
	return s, ok
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
