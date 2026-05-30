package runtime

// TrackOrder records a placed order in the orderID→handID map for WS fill routing.
// The key is the order's clid (for bot orders, tracked before PlaceOrder) or its
// exchange id (bracket orders, reconcile). See CLIENT_ORDER_ID.md.
func (r *HelmRuntime) TrackOrder(orderID, handID string) {
	r.orderHandMapMu.Lock()
	r.orderHandMap[orderID] = handID
	r.orderHandMapMu.Unlock()
}

// RemoveOrderTracking removes a completed or canceled order from the tracking map.
func (r *HelmRuntime) RemoveOrderTracking(orderID string) {
	r.orderHandMapMu.Lock()
	delete(r.orderHandMap, orderID)
	r.orderHandMapMu.Unlock()
}

// FillRouteCounts returns the cumulative count of WS fills resolved via the clid,
// the exchange-id alias, and the orphan path. Used by /metrics for rollout visibility
// of the client-order-id migration. See CLIENT_ORDER_ID.md.
func (r *HelmRuntime) FillRouteCounts() (clid, alias, orphan int64) {
	return r.fillRouteClid.Load(), r.fillRouteAlias.Load(), r.fillRouteOrphan.Load()
}

// HasOrderTracking reports whether an order is currently tracked.
func (r *HelmRuntime) HasOrderTracking(orderID string) bool {
	r.orderHandMapMu.RLock()
	_, ok := r.orderHandMap[orderID]
	r.orderHandMapMu.RUnlock()
	return ok
}

// PendingOrderHandID returns the handID that placed the given order, or "" if unknown.
func (r *HelmRuntime) PendingOrderHandID(orderID string) string {
	r.orderHandMapMu.RLock()
	handID := r.orderHandMap[orderID]
	r.orderHandMapMu.RUnlock()
	return handID
}
