package runtime

import (
	"sync"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// ── exchangePublicData ────────────────────────────────────────────────────────

// exchangePublicData holds all registry-owned public market data for one exchange.
// "Public" means data fetched from the exchange's public API — no credentials needed:
//   - filters: LOT_SIZE, PRICE_FILTER, MIN_NOTIONAL (fetched once at prewarm)
//   - prices:  last-trade price per symbol (updated on every tick)
//
// All fields are keyed by bare symbol (e.g. "ETHUSDT"), no exchange prefix.
// One instance per exchange name (e.g. "binance", "okx") in exchangeMarketCache.
// A single RWMutex guards all fields.
type exchangePublicData struct {
	mu sync.RWMutex

	// symbol → LOT_SIZE / PRICE_FILTER / MIN_NOTIONAL.
	// Written once by PrewarmFilters at startup, then read-only.
	filters map[string]exchange.SymbolFilters

	// symbol → last-trade price; updated on every market tick.
	prices map[string]decimal.Decimal
}

func newExchangePublicData() *exchangePublicData {
	return &exchangePublicData{
		filters: make(map[string]exchange.SymbolFilters),
		prices:  make(map[string]decimal.Decimal),
	}
}

// ── filters ───────────────────────────────────────────────────────────────────

// setFilter writes a single symbol's filters (called from PrewarmFilters goroutines).
func (d *exchangePublicData) setFilter(symbol string, f exchange.SymbolFilters) {
	d.mu.Lock()
	d.filters[symbol] = f
	d.mu.Unlock()
}

// filterView returns this exchangePublicData as a SymbolFilterStore.
// HelmRuntime wires this in at Spawn so callers only need the symbol.
// Unlike the old exchangeFilterMap alias, this is concurrency-safe and
// supports SetFilters for lazy on-demand fetches.
func (d *exchangePublicData) filterView() SymbolFilterStore {
	return d
}

// GetFilters returns the cached SymbolFilters for symbol (satisfies SymbolFilterStore).
func (d *exchangePublicData) GetFilters(symbol string) exchange.SymbolFilters {
	d.mu.RLock()
	f := d.filters[symbol]
	d.mu.RUnlock()
	return f
}

// SetFilters caches a fetched SymbolFilters entry (satisfies SymbolFilterStore).
// Called by filtersFor on a lazy cache-miss fetch and by PrewarmFilters.
func (d *exchangePublicData) SetFilters(symbol string, f exchange.SymbolFilters) {
	d.setFilter(symbol, f)
}

// ── prices ────────────────────────────────────────────────────────────────────

// setPrice updates the last-trade price for a symbol.
func (d *exchangePublicData) setPrice(symbol string, price decimal.Decimal) {
	d.mu.Lock()
	d.prices[symbol] = price
	d.mu.Unlock()
}

// getPrice returns the cached last-trade price for symbol, or zero if not yet seen.
func (d *exchangePublicData) getPrice(symbol string) decimal.Decimal {
	d.mu.RLock()
	p := d.prices[symbol]
	d.mu.RUnlock()
	return p
}

// ── exchangeMarketCache ───────────────────────────────────────────────────────

// exchangeMarketCache is the registry-owned collection of per-exchange public data.
// Outer key = exchange name (e.g. "binance", "okx").
// The outer map is written only at Spawn time (under mu); inner reads go to
// exchangePublicData directly.
type exchangeMarketCache struct {
	mu        sync.RWMutex
	exchanges map[string]*exchangePublicData
}

func newExchangeMarketCache() exchangeMarketCache {
	return exchangeMarketCache{
		exchanges: make(map[string]*exchangePublicData),
	}
}

// forExchange returns the public data bucket for the given exchange, creating it
// if it does not exist. Safe to call concurrently.
func (c *exchangeMarketCache) forExchange(name string) *exchangePublicData {
	c.mu.RLock()
	d := c.exchanges[name]
	c.mu.RUnlock()
	if d != nil {
		return d
	}
	c.mu.Lock()
	if c.exchanges[name] == nil {
		c.exchanges[name] = newExchangePublicData()
	}
	d = c.exchanges[name]
	c.mu.Unlock()
	return d
}

// filterViewFor returns a SymbolFilterStore for the given exchange.
func (c *exchangeMarketCache) filterViewFor(exchangeName string) SymbolFilterStore {
	return c.forExchange(exchangeName).filterView()
}

// setFilter stores a fetched SymbolFilters entry (called from PrewarmFilters goroutines).
func (c *exchangeMarketCache) setFilter(exchangeName, symbol string, f exchange.SymbolFilters) {
	c.forExchange(exchangeName).setFilter(symbol, f)
}

// priceDataFor returns the exchangePublicData for the given exchange.
// The returned pointer is wired into HelmRuntime at Spawn — all helms on the same
// exchange share one data bucket and see price/L2 updates immediately.
func (c *exchangeMarketCache) priceDataFor(exchangeName string) *exchangePublicData {
	return c.forExchange(exchangeName)
}

// updatePrice writes a tick into the correct exchange's price map.
func (c *exchangeMarketCache) updatePrice(exchangeName, bareSym string, price decimal.Decimal) {
	c.mu.RLock()
	d := c.exchanges[exchangeName]
	c.mu.RUnlock()
	if d != nil {
		d.setPrice(bareSym, price)
	}
}
