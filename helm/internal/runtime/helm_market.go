package runtime

import (
	"log/slog"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// UpdatePrice stores the latest market price for a symbol and forwards it to the portfolio.
// symbol is already a bare ticker (the herald "exchange:" prefix is stripped by
// Registry.UpdatePrice before the value reaches the shared exchangePriceMap).
func (r *HelmRuntime) UpdatePrice(symbol string, price decimal.Decimal) {
	if !price.IsPositive() {
		return
	}
	r.marketData.setPrice(symbol, price)
	r.Portfolio.UpdatePrice(symbol, price)
}

func (r *HelmRuntime) lastKnownPrice(symbol string) decimal.Decimal {
	// exchangePriceMap covers the trade cache + L2 mid-price; the Portfolio is the final fallback.
	if p := r.marketData.getPrice(symbol); p.IsPositive() {
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
	return r.marketData.latestL2(symbol)
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
