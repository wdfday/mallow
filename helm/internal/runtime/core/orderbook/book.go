// Package orderbook provides two independent utilities:
//
//  1. Exchange constraint validation (OrderBook interface + orderBook impl):
//     Validates a proposed order against per-symbol exchange rules — minimum qty,
//     max qty, step size, minimum notional value, and active/halted status.
//     Symbol constraints are registered once at startup from exchange metadata.
//
//  2. Market-data history (BookUpdate ring buffer):
//     Each registered symbol maintains a fixed-cap circular buffer of recent
//     bid/ask/last snapshots. Callers can retrieve the latest update or a
//     chronological window via LatestUpdate / RecentUpdates.
//
//  3. SpreadTracker (spread.go):
//     A lightweight rolling average of bid-ask spreads, embeddable in any struct.
//     Used by the Tactician for stable limit-price calculation.
//
// Order tracking (orderID → handID routing) lives in HelmRuntime, not here.
package orderbook

import (
	"fmt"
	"slices"
	"sync"

	"github.com/shopspring/decimal"
)

const defaultSymbolHistoryCapacity = 32

// OrderBook is the interface for exchange-level order constraint validation
// and per-symbol market-data history. Shared per broker type across all runtimes.
type OrderBook interface {
	// RegisterSymbol registers or updates exchange constraints for a symbol.
	RegisterSymbol(info SymbolInfo)
	// RegisterSymbols bulk-registers exchange constraints.
	RegisterSymbols(infos []SymbolInfo)
	// Validate checks a proposed order against exchange constraints.
	// Returns AdjustedQty rounded to the symbol's step size on success.
	Validate(order ProposedOrder) ValidationResult
	// SupportedSymbols returns all registered symbols in sorted order.
	SupportedSymbols() []string
	// RecordUpdate appends a market-data observation to the symbol's history buffer.
	RecordUpdate(update BookUpdate) error
	// LatestUpdate returns the most recent observation for symbol.
	LatestUpdate(symbol string) (BookUpdate, bool)
	// RecentUpdates returns up to limit observations in chronological order.
	RecentUpdates(symbol string, limit int) []BookUpdate
}

// orderBook is the default in-memory implementation.
type orderBook struct {
	broker  string // e.g. "binance", "alpaca"
	mu      sync.RWMutex
	symbols map[string]*symbolState
}

// NewOrderBook creates an empty in-memory OrderBook for the given broker.
func NewOrderBook(broker string) OrderBook {
	return &orderBook{
		broker:  broker,
		symbols: make(map[string]*symbolState),
	}
}

// RegisterSymbol registers or updates exchange constraints for a symbol.
func (ob *orderBook) RegisterSymbol(info SymbolInfo) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	state, ok := ob.symbols[info.Symbol]
	if !ok {
		state = &symbolState{history: newBookUpdateRing(defaultSymbolHistoryCapacity)}
		ob.symbols[info.Symbol] = state
	}
	state.info = info
}

// RegisterSymbols bulk-registers exchange constraints.
func (ob *orderBook) RegisterSymbols(infos []SymbolInfo) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	for _, info := range infos {
		state, ok := ob.symbols[info.Symbol]
		if !ok {
			state = &symbolState{history: newBookUpdateRing(defaultSymbolHistoryCapacity)}
			ob.symbols[info.Symbol] = state
		}
		state.info = info
	}
}

// Validate checks a proposed order against registered exchange constraints.
// Returns ValidationResult.AdjustedQty rounded to the symbol's step size on success.
func (ob *orderBook) Validate(order ProposedOrder) ValidationResult {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	state, ok := ob.symbols[order.Symbol]
	if !ok {
		return ValidationResult{Valid: false, Reason: fmt.Sprintf("symbol %q not supported", order.Symbol)}
	}
	info := state.info
	if !info.Active {
		return ValidationResult{Valid: false, Reason: fmt.Sprintf("symbol %q is halted/inactive", order.Symbol)}
	}
	if !order.Qty.IsPositive() {
		return ValidationResult{Valid: false, Reason: "quantity must be positive"}
	}
	if order.Qty.LessThan(info.MinQty) {
		return ValidationResult{Valid: false, Reason: fmt.Sprintf("qty %s below min %s", order.Qty, info.MinQty)}
	}
	if info.MaxQty.IsPositive() && order.Qty.GreaterThan(info.MaxQty) {
		return ValidationResult{Valid: false, Reason: fmt.Sprintf("qty %s exceeds max %s", order.Qty, info.MaxQty)}
	}

	adjustedQty := roundToStep(order.Qty, info.StepSize)
	if adjustedQty.LessThan(info.MinQty) {
		return ValidationResult{Valid: false, Reason: fmt.Sprintf("qty rounds to %s, below min %s", adjustedQty, info.MinQty)}
	}

	if order.Price.IsPositive() && info.MinNotional.IsPositive() {
		notional := adjustedQty.Mul(order.Price)
		if notional.LessThan(info.MinNotional) {
			return ValidationResult{Valid: false, Reason: fmt.Sprintf("notional %s below min %s", notional, info.MinNotional)}
		}
	}

	return ValidationResult{Valid: true, AdjustedQty: adjustedQty}
}

// SupportedSymbols returns all registered symbols in sorted order.
func (ob *orderBook) SupportedSymbols() []string {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	out := make([]string, 0, len(ob.symbols))
	for symbol := range ob.symbols {
		out = append(out, symbol)
	}
	slices.Sort(out)
	return out
}

// RecordUpdate appends a market-data snapshot to the symbol's history buffer.
// Returns an error if the symbol is not registered.
func (ob *orderBook) RecordUpdate(update BookUpdate) error {
	if update.Symbol == "" {
		return fmt.Errorf("symbol is required")
	}
	ob.mu.Lock()
	defer ob.mu.Unlock()
	state, ok := ob.symbols[update.Symbol]
	if !ok {
		return fmt.Errorf("symbol %q not supported", update.Symbol)
	}
	state.history.push(update)
	return nil
}

// LatestUpdate returns the most recent market-data snapshot for symbol.
func (ob *orderBook) LatestUpdate(symbol string) (BookUpdate, bool) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	state, ok := ob.symbols[symbol]
	if !ok {
		return BookUpdate{}, false
	}
	return state.history.latest()
}

// RecentUpdates returns up to limit snapshots in chronological order (oldest first).
func (ob *orderBook) RecentUpdates(symbol string, limit int) []BookUpdate {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	state, ok := ob.symbols[symbol]
	if !ok {
		return nil
	}
	return state.history.snapshot(limit)
}

// ─────────────────────────────────────────────────────────────────────────────

// roundToStep rounds qty down to the nearest multiple of step.
// Returns qty unchanged if step is zero or negative.
func roundToStep(qty, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return qty
	}
	return qty.Div(step).Floor().Mul(step)
}
