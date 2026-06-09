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

// priceTick returns the number of meaningful decimal places for a step size.
// Exchanges often return stepSize with trailing zeros (e.g. "0.010000") which
// makes Decimal.Exponent() unreliable (-6 instead of -2). We normalise via
// float64 → shortest string to strip the trailing zeros before counting.
//
//	"0.010000" → 0.01 → "0.01" → 2 dp
//	"0.00010000" → 0.0001 → "0.0001" → 4 dp
//
// Falls back to 2 when step is zero/unknown. Capped at 8.
func priceTick(step decimal.Decimal) int32 {
	if !step.IsPositive() {
		return 2
	}
	f, _ := step.Float64()
	s := strconv.FormatFloat(f, 'f', -1, 64) // shortest decimal form, no trailing zeros
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

// SymbolFilterStore is a small interface for looking up exchange precision rules.
// The registry wires a per-exchange view into each HelmRuntime at spawn time —
// same pattern as l2Books — so callers only need the symbol, not the exchange name.
type SymbolFilterStore interface {
	GetFilters(symbol string) exchange.SymbolFilters
	// SetFilters caches a fetched entry. Used by filtersFor on lazy cache-miss.
	SetFilters(symbol string, f exchange.SymbolFilters)
}

// filtersFor looks up SymbolFilters via the injected per-exchange store.
// On a cache miss (QtyStep == 0, symbol not prewarm-ed), it fetches from the
// exchange on-demand with a short timeout and caches the result. This prevents
// LOT_SIZE errors when a new symbol starts trading before PrewarmFilters ran.
func (r *HelmRuntime) filtersFor(ctx context.Context, symbol string) exchange.SymbolFilters {
	if r.FilterStore == nil {
		return exchange.SymbolFilters{}
	}
	f := r.FilterStore.GetFilters(symbol)
	if f.QtyStep.IsPositive() {
		return f
	}
	// Cache miss — lazy fetch from exchange.
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
//
// Uses floor division: floor(qty / stepSize) × stepSize.
// This is always a valid multiple of stepSize regardless of how many decimal
// places the exchange uses — no float64 conversion, no string parsing.
//
//	SOL  stepSize=0.01      qty=3.14159  → 3.14
//	ETH  stepSize=0.0001    qty=0.12345  → 0.1234
//	DOGE stepSize=1         qty=152.7    → 152
//	BTC  stepSize=0.00001   qty=0.000049 → 0.00004
//
// Falls back to 8 dp truncation when filters are unavailable (prewarm not done).
func truncateQty(filters exchange.SymbolFilters, qty decimal.Decimal) decimal.Decimal {
	if filters.QtyStep.IsPositive() {
		return qty.Div(filters.QtyStep).Floor().Mul(filters.QtyStep)
	}
	return qty.Truncate(8)
}
