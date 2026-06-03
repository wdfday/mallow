package runtime

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// This file holds the small single-responsibility state owners that HelmRuntime composes.
// Each owns exactly one cohesive sub-state plus the mutex that guards it, so the lock
// boundary is the type boundary (a caller can't hold one helper's lock while touching
// another's). HelmRuntime keeps its public methods and just delegates — same locking
// granularity as before, but the state is encapsulated and unit-testable in isolation.
// See ACTOR_MODEL.md (SRP cleanup of the flat runtime).

// ── exchangePriceMap ─────────────────────────────────────────────────────────────

// exchangePriceMap is the registry-owned, per-exchange last-trade price cache.
// Each HelmRuntime gets a pointer to its exchange's map at Spawn time, so all helms
// on the same exchange share one price feed — no per-helm copy needed.
// getL2 delegates to the registry's shared broker-level L2 cache; nil = no streamer.
type exchangePriceMap struct {
	mu     sync.RWMutex
	prices map[string]decimal.Decimal
	// getL2 delegates to the registry's shared broker-level cache; nil = no L2 streamer.
	getL2 func(symbol string) (exchange.L2Snapshot, bool)
}

func newExchangePriceMap() *exchangePriceMap {
	return &exchangePriceMap{prices: make(map[string]decimal.Decimal)}
}

func (m *exchangePriceMap) set(symbol string, price decimal.Decimal) {
	m.mu.Lock()
	m.prices[symbol] = price
	m.mu.Unlock()
}

// get returns the cached price, falling back to the L2 mid-price. Returns zero when
// neither is available (the Portfolio fallback is the caller's responsibility).
func (m *exchangePriceMap) get(symbol string) decimal.Decimal {
	m.mu.RLock()
	p := m.prices[symbol]
	m.mu.RUnlock()
	if p.IsPositive() {
		return p
	}
	if m.getL2 != nil {
		if snap, ok := m.getL2(symbol); ok {
			bid := snap.Bids[0].Price
			ask := snap.Asks[0].Price
			if bid.IsPositive() && ask.IsPositive() {
				return bid.Add(ask).Div(decimal.NewFromInt(2))
			}
		}
	}
	return decimal.Zero
}

func (m *exchangePriceMap) latestL2(symbol string) (exchange.L2Snapshot, bool) {
	if m.getL2 == nil {
		return exchange.L2Snapshot{}, false
	}
	return m.getL2(symbol)
}

// ── orderRouter ─────────────────────────────────────────────────────────────────

// orderRouter maps an order key (clid for bot orders, exchange id for brackets/reconcile)
// to the handID that owns it, for WS fill routing. See CLIENT_ORDER_ID.md.
type orderRouter struct {
	mu sync.RWMutex
	m  map[string]string
}

func newOrderRouter() *orderRouter { return &orderRouter{m: make(map[string]string)} }

func (o *orderRouter) track(orderID, handID string) {
	o.mu.Lock()
	o.m[orderID] = handID
	o.mu.Unlock()
}

func (o *orderRouter) remove(orderID string) {
	o.mu.Lock()
	delete(o.m, orderID)
	o.mu.Unlock()
}

func (o *orderRouter) has(orderID string) bool {
	o.mu.RLock()
	_, ok := o.m[orderID]
	o.mu.RUnlock()
	return ok
}

func (o *orderRouter) handID(orderID string) string {
	o.mu.RLock()
	id := o.m[orderID]
	o.mu.RUnlock()
	return id
}

// ── dustLedger ──────────────────────────────────────────────────────────────────

// dustLedger tracks the sub-step residual left after a spot exit order is placed with
// truncated qty, so checkPositionDesync doesn't mistake it for an external close.
type dustLedger struct {
	mu sync.Mutex
	m  map[string]decimal.Decimal
}

func newDustLedger() *dustLedger { return &dustLedger{m: make(map[string]decimal.Decimal)} }

func (d *dustLedger) record(symbol string, qty decimal.Decimal) {
	if qty.IsZero() || qty.IsNegative() {
		return
	}
	d.mu.Lock()
	d.m[symbol] = d.m[symbol].Add(qty)
	d.mu.Unlock()
}

func (d *dustLedger) clear(symbol string) {
	d.mu.Lock()
	delete(d.m, symbol)
	d.mu.Unlock()
}

func (d *dustLedger) get(symbol string) decimal.Decimal {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.m[symbol]
}

// ── fillDedup ───────────────────────────────────────────────────────────────────

// fillDedup is the idempotency bookkeeping for fill processing: trade IDs already applied
// by gap recovery, and order IDs whose trade.filled was already published. Both maps are
// bounded and reset wholesale when full (a brief re-dedup window is acceptable — it needs
// tens of thousands of fills between two 5-minute Sync cycles, which never happens).
type fillDedup struct {
	tradesMu sync.Mutex
	trades   map[string]struct{}
	fillsMu  sync.Mutex
	fills    map[string]struct{}
}

const (
	maxProcessedTrades     = 50_000
	maxProcessedOrderFills = 10_000
)

func newFillDedup() *fillDedup {
	return &fillDedup{trades: make(map[string]struct{}), fills: make(map[string]struct{})}
}

func (f *fillDedup) hasTrade(tradeID string) bool {
	if tradeID == "" {
		return false
	}
	f.tradesMu.Lock()
	_, ok := f.trades[tradeID]
	f.tradesMu.Unlock()
	return ok
}

func (f *fillDedup) markTrade(tradeID string) {
	if tradeID == "" {
		return
	}
	f.tradesMu.Lock()
	if len(f.trades) >= maxProcessedTrades {
		f.trades = make(map[string]struct{}, maxProcessedTrades)
	}
	f.trades[tradeID] = struct{}{}
	f.tradesMu.Unlock()
}

func (f *fillDedup) hasFillPublished(orderID string) bool {
	if orderID == "" {
		return false
	}
	f.fillsMu.Lock()
	_, ok := f.fills[orderID]
	f.fillsMu.Unlock()
	return ok
}

func (f *fillDedup) markFillPublished(orderID string) {
	if orderID == "" {
		return
	}
	f.fillsMu.Lock()
	if len(f.fills) >= maxProcessedOrderFills {
		f.fills = make(map[string]struct{}, maxProcessedOrderFills)
	}
	f.fills[orderID] = struct{}{}
	f.fillsMu.Unlock()
}

func timePtr(t time.Time) *time.Time { return &t }

func unmarshalJSON(b []byte, v any) error {
	return json.Unmarshal(b, v)
}
