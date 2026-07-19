package market

import (
	"context"
	"log/slog"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/safe"
	"sync"
)

// SymbolFilterStore is a small interface for looking up exchange precision rules.
// The registry wires a per-exchange view into each HelmRuntime at spawn time so
// callers only need the symbol, not the exchange name.
type SymbolFilterStore interface {
	GetFilters(symbol string) exchange.SymbolFilters
	SetFilters(symbol string, f exchange.SymbolFilters)
}

// PrewarmFilters fetches symbol filters for (exchange, symbol) pairs owned by
// active hands. symbolsByExchange must be obtained from the hand service so that
// only pairs that actually belong to each exchange are fetched — OKX symbols
// (SOL-USDT) are never sent to Binance and vice-versa.
//
// As a side-effect it also syncs each exchange's server time (for exchanges that
// implement TimeSyncer) so subsequent signed REST requests carry the correct
// timestamp and avoid -1021 recvWindow errors from server clock drift.
func (c *MarketContext) PrewarmFilters(ctx context.Context, symbolsByExchange map[exchange.Exchange][]string) {
	// ── 1. Sync server time (sequential — one call per exchange) ──────────────
	for ex := range symbolsByExchange {
		if ts, ok := ex.(exchange.TimeSyncer); ok {
			if err := ts.SyncTime(ctx); err != nil {
				slog.Warn("prewarm: server time sync failed", "exchange", ex.Name(), "err", err)
			}
		}
	}

	// ── 2. Fetch symbol filters concurrently ──────────────────────────────────
	var wg sync.WaitGroup
	for ex, symbols := range symbolsByExchange {
		sip, ok := ex.(exchange.SymbolInfoProvider)
		if !ok {
			continue
		}
		c.FilterViewFor(ex.Name())
		for _, sym := range symbols {
			wg.Add(1)
			go func(p exchange.SymbolInfoProvider, exName, s string) {
				defer safe.Recover()
				defer wg.Done()
				f, err := p.GetSymbolFilters(ctx, s)
				if err != nil {
					slog.Warn("prewarm: symbol filters fetch failed",
						"exchange", exName, "symbol", s, "err", err)
					return
				}
				c.setFilter(exName, s, f)
				slog.Info("prewarm: symbol filters ready",
					"exchange", exName, "symbol", s,
					"qty_step", f.QtyStep, "price_tick", f.PriceTick,
					"min_qty", f.MinQty, "min_notional", f.MinNotional)
			}(sip, ex.Name(), sym)
		}
	}
	wg.Wait()
}
