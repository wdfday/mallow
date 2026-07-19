package actor

import (
	"encoding/json"
	"sync"
)

// This file holds the small single-responsibility state owners that HelmRuntime composes.
// Each owns exactly one cohesive sub-state plus the mutex that guards it, so the lock
// boundary is the type boundary (a caller can't hold one helper's lock while touching
// another's). HelmRuntime keeps its public methods and just delegates — same locking
// granularity as before, but the state is encapsulated and unit-testable in isolation.
// See ACTOR_MODEL.md (SRP cleanup of the flat runtime).

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

func unmarshalJSON(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

// ── HelmRuntime order-routing methods ────────────────────────────────────────

// TrackOrder records a placed order in the orderID→handID map for WS fill routing.
// The key is the order's clid (for bot orders, tracked before PlaceOrder) or its
// exchange id (bracket orders, reconcile). See CLIENT_ORDER_ID.md.
func (r *HelmRuntime) TrackOrder(orderID, handID string) { r.router.track(orderID, handID) }

// RemoveOrderTracking removes a completed or canceled order from the tracking map.
func (r *HelmRuntime) RemoveOrderTracking(orderID string) { r.router.remove(orderID) }

// FillRouteCounts returns the cumulative count of WS fills resolved via the clid,
// the exchange-id alias, and the orphan path. Used by /metrics for rollout visibility
// of the client-order-id migration. See CLIENT_ORDER_ID.md.
func (r *HelmRuntime) FillRouteCounts() (clid, alias, orphan int64) {
	return r.fillRouteClid.Load(), r.fillRouteAlias.Load(), r.fillRouteOrphan.Load()
}

// HasOrderTracking reports whether an order is currently tracked.
func (r *HelmRuntime) HasOrderTracking(orderID string) bool { return r.router.has(orderID) }

// PendingOrderHandID returns the handID that placed the given order, or "" if unknown.
func (r *HelmRuntime) PendingOrderHandID(orderID string) string { return r.router.handID(orderID) }
