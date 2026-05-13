package orderbook

// NewNoopOrderBook returns an OrderBook that accepts all orders and tracks nothing.
// Use while order-constraint validation and duplicate-detection are disabled.
func NewNoopOrderBook() OrderBook { return noopOrderBook{} }

type noopOrderBook struct{}

func (noopOrderBook) RegisterSymbol(_ SymbolInfo)                {}
func (noopOrderBook) RegisterSymbols(_ []SymbolInfo)             {}
func (noopOrderBook) TrackOrder(_ PendingOrder)                  {}
func (noopOrderBook) RemoveOrder(_, _ string)                    {}
func (noopOrderBook) Has(_, _ string) bool                       { return false }
func (noopOrderBook) PendingOrders(_ string) []PendingOrder      { return nil }
func (noopOrderBook) SupportedSymbols() []string                 { return nil }
func (noopOrderBook) RecordUpdate(_ BookUpdate) error            { return nil }
func (noopOrderBook) LatestUpdate(_ string) (BookUpdate, bool)   { return BookUpdate{}, false }
func (noopOrderBook) RecentUpdates(_ string, _ int) []BookUpdate { return nil }
func (noopOrderBook) Validate(_ ProposedOrder) ValidationResult {
	return ValidationResult{Valid: true}
}
