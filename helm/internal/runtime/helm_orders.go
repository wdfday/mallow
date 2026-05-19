package runtime

// TrackOrder records a placed order in the orderID→handID map for WS fill routing.
func (r *HelmRuntime) TrackOrder(orderID, handID string) {
	r.orderHandMapMu.Lock()
	r.orderHandMap[orderID] = handID
	r.orderHandMapMu.Unlock()
}

// RemoveOrderTracking removes a completed or cancelled order from the tracking map.
func (r *HelmRuntime) RemoveOrderTracking(orderID string) {
	r.orderHandMapMu.Lock()
	delete(r.orderHandMap, orderID)
	r.orderHandMapMu.Unlock()
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
