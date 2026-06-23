package runtime

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

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

// ── Symbol filter utilities ───────────────────────────────────────────────────

// SymbolFilterStore is a small interface for looking up exchange precision rules.
// The registry wires a per-exchange view into each HelmRuntime at spawn time so
// callers only need the symbol, not the exchange name.
type SymbolFilterStore interface {
	GetFilters(symbol string) exchange.SymbolFilters
	SetFilters(symbol string, f exchange.SymbolFilters)
}

// filtersFor looks up SymbolFilters via the injected per-exchange store.
// On a cache miss (QtyStep == 0, symbol not prewarm-ed), it fetches from the
// exchange on-demand with a short timeout and caches the result.
func (r *HelmRuntime) filtersFor(ctx context.Context, symbol string) exchange.SymbolFilters {
	if r.FilterStore == nil {
		return exchange.SymbolFilters{}
	}
	f := r.FilterStore.GetFilters(symbol)
	if f.QtyStep.IsPositive() {
		return f
	}
	sip, ok := r.Exchange.(exchange.SymbolInfoProvider)
	if !ok {
		return f
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fetched, err := sip.GetSymbolFilters(fetchCtx, symbol)
	if err != nil {
		slog.Warn("filtersFor: on-demand filter fetch failed",
			"symbol", symbol, "err", err)
		return f
	}
	slog.Info("filtersFor: lazy-fetched symbol filters",
		"symbol", symbol,
		"qty_step", fetched.QtyStep, "price_tick", fetched.PriceTick)
	r.FilterStore.SetFilters(symbol, fetched)
	return fetched
}

// truncateQty rounds qty DOWN to the nearest valid LOT_SIZE multiple.
func truncateQty(filters exchange.SymbolFilters, qty decimal.Decimal) decimal.Decimal {
	if filters.QtyStep.IsPositive() {
		return qty.Div(filters.QtyStep).Floor().Mul(filters.QtyStep)
	}
	return qty.Truncate(8)
}

// priceTick returns the number of meaningful decimal places for a step size.
// Normalises via float64 → shortest string to strip trailing zeros before counting.
func priceTick(step decimal.Decimal) int32 {
	if !step.IsPositive() {
		return 2
	}
	f, _ := step.Float64()
	s := strconv.FormatFloat(f, 'f', -1, 64)
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return 0
	}
	prec := int32(len(s) - dot - 1)
	if prec > 8 {
		return 8
	}
	return prec
}
