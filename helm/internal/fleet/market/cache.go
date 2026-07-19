// Package market owns every public (no-credential) market data point helm consumes:
// last-trade price, symbol precision/notional filters, and L2 order-book snapshots.
//
// It is the single source of truth for this data. Exactly one WebSocket connection
// per exchange (see stream.go) writes into the MarketContext; every other part of helm
// (HelmRuntime, hand runners, the trading path) only reads through the accessors
// below. It self-connects directly to each exchange — no dependency on herald/NATS.
package market

import (
	"mallow/helm/internal/infra/exchange"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// bookRingCap is how many L2 snapshots DepthRing keeps per symbol — see
// exchange.DepthRing's own doc comment for why 16 (~1.6s of history at
// @100ms cadence) is the right window for spread/slippage estimation.

// PricePoint is the last-trade price for a symbol plus when it was observed,
// so callers can reason about staleness instead of trusting a bare decimal.
type PricePoint struct {
	Price     decimal.Decimal
	UpdatedAt time.Time
}

// ── ExchangeData ──────────────────────────────────────────────────────────────

// ExchangeData holds all registry-owned public market data for one exchange.
// "Public" means data fetched from the exchange's public API/WebSocket — no
// credentials needed:
//   - filters: LOT_SIZE, PRICE_FILTER, MIN_NOTIONAL (fetched once at prewarm,
//     refreshed lazily on cache miss)
//   - prices:  last-trade price per symbol, updated on every market tick
//   - books:   top-5 bid/ask snapshots per symbol, updated on every book push
//
// All fields are keyed by bare symbol (e.g. "ETHUSDT"), no exchange prefix.
// One instance per exchange name (e.g. "binance", "okx") in MarketContext.
// A single RWMutex guards all fields.
type ExchangeData struct {
	mu sync.RWMutex

	// symbol → LOT_SIZE / PRICE_FILTER / MIN_NOTIONAL.
	// Written once by PrewarmFilters at startup, then read-mostly (a cache-miss
	// lazy fetch may add an entry later — see runtime.filtersFor).
	filters map[string]exchange.SymbolFilters

	// symbol → last-trade price; updated on every market tick.
	prices map[string]PricePoint

	// symbol → ring of recent L2 top-5 snapshots; updated on every book push.
	// Created lazily on first push so symbols with no book coverage cost nothing.
	books map[string]*exchange.DepthRing
}

// NewExchangeData creates an empty ExchangeData bucket. Exposed for
// HelmRuntime's zero-value default (overwritten by Registry.Spawn with the
// shared per-exchange bucket from MarketContext.PriceDataFor) and for tests.
func NewExchangeData() *ExchangeData {
	return newExchangeData()
}

func newExchangeData() *ExchangeData {
	return &ExchangeData{
		filters: make(map[string]exchange.SymbolFilters),
		prices:  make(map[string]PricePoint),
		books:   make(map[string]*exchange.DepthRing),
	}
}

// ── filters ───────────────────────────────────────────────────────────────────

// setFilter writes a single symbol's filters (called from PrewarmFilters goroutines).
func (d *ExchangeData) setFilter(symbol string, f exchange.SymbolFilters) {
	d.mu.Lock()
	d.filters[symbol] = f
	d.mu.Unlock()
}

// filterView returns this ExchangeData as a SymbolFilterStore.
// HelmRuntime wires this in at Spawn so callers only need the symbol.
func (d *ExchangeData) filterView() SymbolFilterStore {
	return d
}

// GetFilters returns the cached SymbolFilters for symbol (satisfies SymbolFilterStore).
func (d *ExchangeData) GetFilters(symbol string) exchange.SymbolFilters {
	d.mu.RLock()
	f := d.filters[symbol]
	d.mu.RUnlock()
	return f
}

// SetFilters caches a fetched SymbolFilters entry (satisfies SymbolFilterStore).
// Called by runtime.filtersFor on a lazy cache-miss fetch and by PrewarmFilters.
func (d *ExchangeData) SetFilters(symbol string, f exchange.SymbolFilters) {
	d.setFilter(symbol, f)
}

// ── prices ────────────────────────────────────────────────────────────────────

// SetPrice updates the last-trade price for a symbol. Exported: the streaming
// WebSocket (stream.go) is the primary writer, but a few execution-path
// bootstrap/audit writes (fill price, REST cache-miss fallback, portfolio
// sync) also call this directly — see runtime/helm_trading.go and
// runtime/helm_sync.go. Those are one-off writes tied to a specific trade,
// not a competing continuous feed, so they don't violate single-source-of-truth
// for the streaming price.
func (d *ExchangeData) SetPrice(symbol string, price decimal.Decimal) {
	d.mu.Lock()
	d.prices[symbol] = PricePoint{Price: price, UpdatedAt: time.Now()}
	d.mu.Unlock()
}

// GetPrice returns the cached last-trade price for symbol, or zero if not yet seen.
func (d *ExchangeData) GetPrice(symbol string) decimal.Decimal {
	d.mu.RLock()
	p := d.prices[symbol]
	d.mu.RUnlock()
	return p.Price
}

// GetPricePoint returns the cached price plus its observation time.
func (d *ExchangeData) GetPricePoint(symbol string) (PricePoint, bool) {
	d.mu.RLock()
	p, ok := d.prices[symbol]
	d.mu.RUnlock()
	return p, ok
}

// ── books ─────────────────────────────────────────────────────────────────────

// bookRing returns (creating if needed) the DepthRing for symbol.
func (d *ExchangeData) bookRing(symbol string) *exchange.DepthRing {
	d.mu.RLock()
	r := d.books[symbol]
	d.mu.RUnlock()
	if r != nil {
		return r
	}
	d.mu.Lock()
	r = d.books[symbol]
	if r == nil {
		r = &exchange.DepthRing{}
		d.books[symbol] = r
	}
	d.mu.Unlock()
	return r
}

// PushBook records a new L2 top-5 snapshot for symbol (called by the streaming
// WebSocket on every book push — the sole writer of book data).
func (d *ExchangeData) PushBook(symbol string, snap exchange.L2Snapshot) {
	d.bookRing(symbol).Push(snap)
}

// LatestBook returns the most recent L2 snapshot for symbol, if any has arrived yet.
func (d *ExchangeData) LatestBook(symbol string) (exchange.L2Snapshot, bool) {
	d.mu.RLock()
	r := d.books[symbol]
	d.mu.RUnlock()
	if r == nil {
		return exchange.L2Snapshot{}, false
	}
	return r.Latest()
}

// BookHistory returns up to n recent L2 snapshots for symbol, newest first.
func (d *ExchangeData) BookHistory(symbol string, n int) []exchange.L2Snapshot {
	d.mu.RLock()
	r := d.books[symbol]
	d.mu.RUnlock()
	if r == nil {
		return nil
	}
	return r.Last(n)
}

// ── Cache ─────────────────────────────────────────────────────────────────────

// MarketContext is the registry-owned collection of per-exchange public data.
// Outer key = exchange name (e.g. "binance", "okx").
// The outer map grows only when a new exchange is first seen (Spawn time or
// StartStreaming); inner reads go to ExchangeData directly.
type MarketContext struct {
	mu        sync.RWMutex
	exchanges map[string]*ExchangeData

	// streaming tracks which exchanges already have an active WS listener, so
	// StartStreaming is safe to call again as new exchanges appear (e.g. a new
	// hand hydrates on an exchange with no runtime hands yet at boot).
	streaming map[string]bool
}

// NewMarketContext creates an empty MarketContext.
func NewMarketContext() *MarketContext {
	return &MarketContext{
		exchanges: make(map[string]*ExchangeData),
		streaming: make(map[string]bool),
	}
}

// ForExchange returns the public data bucket for the given exchange, creating it
// if it does not exist. Safe to call concurrently.
func (c *MarketContext) ForExchange(name string) *ExchangeData {
	c.mu.RLock()
	d := c.exchanges[name]
	c.mu.RUnlock()
	if d != nil {
		return d
	}
	c.mu.Lock()
	if c.exchanges[name] == nil {
		c.exchanges[name] = newExchangeData()
	}
	d = c.exchanges[name]
	c.mu.Unlock()
	return d
}

// FilterViewFor returns a SymbolFilterStore for the given exchange.
func (c *MarketContext) FilterViewFor(exchangeName string) SymbolFilterStore {
	return c.ForExchange(exchangeName).filterView()
}

// setFilter stores a fetched SymbolFilters entry (called from PrewarmFilters goroutines).
func (c *MarketContext) setFilter(exchangeName, symbol string, f exchange.SymbolFilters) {
	c.ForExchange(exchangeName).setFilter(symbol, f)
}

// PriceDataFor returns the ExchangeData for the given exchange.
// The returned pointer is wired into HelmRuntime at Spawn — all helms on the same
// exchange share one data bucket and see price/book updates immediately.
func (c *MarketContext) PriceDataFor(exchangeName string) *ExchangeData {
	return c.ForExchange(exchangeName)
}

// UpdatePrice writes a tick into the correct exchange's price map.
// Called by the streaming WebSocket (stream.go) — the sole continuous writer.
func (c *MarketContext) UpdatePrice(exchangeName, bareSym string, price decimal.Decimal) {
	if !price.IsPositive() {
		return
	}
	c.ForExchange(exchangeName).SetPrice(bareSym, price)
}

// UpdateBook writes an L2 snapshot into the correct exchange's book ring.
// Called by the streaming WebSocket (stream.go) — the sole writer of book data.
func (c *MarketContext) UpdateBook(exchangeName, bareSym string, snap exchange.L2Snapshot) {
	c.ForExchange(exchangeName).PushBook(bareSym, snap)
}

// markStreaming records that exchangeName's WS listener is starting.
// Returns false if a listener for this exchange is already running (or being
// started), in which case the caller must not start a second one.
func (c *MarketContext) markStreaming(exchangeName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.streaming[exchangeName] {
		return false
	}
	c.streaming[exchangeName] = true
	return true
}
