package runtime

import (
	"strings"

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

// EnqueueLifecycleEvent appends a broker order lifecycle event to the unbounded
// lifecycle queue and signals the drain goroutine. Never blocks the WS goroutine.
func (r *HelmRuntime) EnqueueLifecycleEvent(ev exchange.OrderLifecycleEvent) {
	r.lifecycleMu.Lock()
	r.lifecycleQueue = append(r.lifecycleQueue, ev)
	r.lifecycleMu.Unlock()
	select {
	case r.lifecycleSignal <- struct{}{}:
	default: // drain already scheduled
	}
}

// EnqueueWsFill appends a WS fill event to the unbounded fill queue and signals
// the drain goroutine. Never blocks the WS goroutine — eliminates backpressure
// that could stall the WS receive loop and delay lifecycle events on the same
// connection when runFillProcessor is slow (e.g. tradeMu contention).
func (r *HelmRuntime) EnqueueWsFill(ev exchange.WsFillEvent) {
	r.wsFillMu.Lock()
	r.wsFillQueue = append(r.wsFillQueue, ev)
	r.wsFillMu.Unlock()
	select {
	case r.wsFillSignal <- struct{}{}:
	default: // drain already scheduled
	}
}

// normalizeCommission returns the commission converted to the quote currency (e.g. USDT)
// and updates the fill quantity (by deducting commission) if the fee is paid in the base asset.
// It uses lastKnownPrice to fetch exchange rates for non-standard assets like BNB.
func (r *HelmRuntime) normalizeCommission(symbol string, side exchange.OrderSide, qty, price, commission decimal.Decimal, asset string) (decimal.Decimal, decimal.Decimal) {
	if !commission.IsPositive() || asset == "" {
		return qty, commission
	}

	assetUpper := strings.ToUpper(asset)
	if assetUpper == "USDT" || assetUpper == "BUSD" || assetUpper == "USDC" {
		return qty, commission
	}

	if side == exchange.Buy && strings.HasPrefix(strings.ToUpper(symbol), assetUpper) {
		qty = qty.Sub(commission)
		if price.IsPositive() {
			commission = commission.Mul(price)
		}
		return qty, commission
	}

	feeSymbol := assetUpper + "USDT"
	feePrice := r.lastKnownPrice(feeSymbol)
	if feePrice.IsZero() && assetUpper == "BNB" {
		feePrice = decimal.NewFromFloat(600.0)
	}
	if feePrice.IsPositive() {
		commission = commission.Mul(feePrice)
	}

	return qty, commission
}
