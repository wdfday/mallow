package actor

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

func (r *HelmRuntime) LastKnownPrice(symbol string) decimal.Decimal {
	// r.MarketData covers the trade cache; the Portfolio is the final fallback.
	if p := r.MarketData.GetPrice(symbol); p.IsPositive() {
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

// NormalizeCommission returns the commission converted to the quote currency (e.g. USDT)
// and updates the fill quantity (by deducting commission) if the fee is paid in the base asset.
//
// For a fee paid in a third asset (exchange-native fee-discount tokens: Binance BNB,
// OKX OKB, Bybit MNT, ...) it looks up {asset}USDT via LastKnownPrice first (cheap,
// no I/O) and falls back to a live REST quote (exchange.PriceFetcher) only on a cache
// miss — this runs on the hand/helm fill-processing path, so a cold cache costs one
// REST round-trip per miss, not per fill; the result is cached via MarketData.SetPrice
// so subsequent fills for the same fee asset hit the warm path. This mirrors the same
// cache-miss-falls-back-to-REST pattern already used in ProcessTrade for entry pricing
// (helm_trading.go) — accepted there for the same reason: blocking occasionally on a
// cold cache beats either a wrong number or an unconverted one.
//
// Previously this only handled BNB, via a hardcoded fallback price (staleness risk) if
// even LastKnownPrice missed, and silently returned commission UNCONVERTED (wrong units,
// no warning) for every other fee asset — including OKB/MNT, which this system's own
// supported exchanges (OKX, Bybit) actually use. Fixed 2026-07-10.
func (r *HelmRuntime) NormalizeCommission(ctx context.Context, symbol string, side exchange.OrderSide, qty, price, commission decimal.Decimal, asset string) (decimal.Decimal, decimal.Decimal) {
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

	// Match the separator convention of the fill's own symbol (e.g. OKX pairs arrive
	// and are queried as "OKB-USDT" — the OKX adapter passes symbol straight through
	// to instId with no dash insertion of its own, see okx/act/price.go tickerLast and
	// okx/act/orders.go PlaceOrder — so a hardcoded no-dash "OKBUSDT" would silently
	// never match a real OKX instrument). Binance-style symbols have no separator.
	sep := ""
	if strings.Contains(symbol, "-") {
		sep = "-"
	}
	feeSymbol := assetUpper + sep + "USDT"
	feePrice := r.LastKnownPrice(feeSymbol)
	if feePrice.IsZero() {
		if pf, ok := r.Exchange.(exchange.PriceFetcher); ok {
			fetchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			p, err := pf.GetCurrentPrice(fetchCtx, r.Creds, feeSymbol)
			cancel()
			if err == nil && p.IsPositive() {
				feePrice = p
				r.MarketData.SetPrice(feeSymbol, p)
			} else {
				slog.Warn("NormalizeCommission: fee asset price unavailable — commission left unconverted",
					"helm_id", r.HelmID, "fee_asset", assetUpper, "fee_symbol", feeSymbol, "err", err)
			}
		} else {
			slog.Warn("NormalizeCommission: fee asset price unavailable, exchange has no PriceFetcher — commission left unconverted",
				"helm_id", r.HelmID, "fee_asset", assetUpper, "fee_symbol", feeSymbol)
		}
	}
	if feePrice.IsPositive() {
		commission = commission.Mul(feePrice)
	}

	return qty, commission
}

// ── Symbol filter utilities ───────────────────────────────────────────────────

// FiltersFor looks up SymbolFilters via the injected per-exchange store.
// On a cache miss (QtyStep == 0, symbol not prewarm-ed), it fetches from the
// exchange on-demand with a short timeout and caches the result.
func (r *HelmRuntime) FiltersFor(ctx context.Context, symbol string) exchange.SymbolFilters {
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
		slog.Warn("FiltersFor: on-demand filter fetch failed",
			"symbol", symbol, "err", err)
		return f
	}
	slog.Info("FiltersFor: lazy-fetched symbol filters",
		"symbol", symbol,
		"qty_step", fetched.QtyStep, "price_tick", fetched.PriceTick)
	r.FilterStore.SetFilters(symbol, fetched)
	return fetched
}
