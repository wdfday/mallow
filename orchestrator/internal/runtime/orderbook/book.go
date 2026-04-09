package orderbook

import (
	"fmt"
	"slices"
	"sync"

	"github.com/shopspring/decimal"
)

// OrderBook is the interface for exchange-level order constraint validation
// and pending order tracking. Shared per broker type across all runtimes.
type OrderBook interface {
	RegisterSymbol(info SymbolInfo)
	RegisterSymbols(infos []SymbolInfo)
	Validate(order ProposedOrder) ValidationResult
	TrackOrder(order PendingOrder)
	RemoveOrder(accountID, orderID string)
	PendingOrders(accountID string) []PendingOrder
	SupportedSymbols() []string
	RecordUpdate(update BookUpdate) error
	LatestUpdate(symbol string) (BookUpdate, bool)
	RecentUpdates(symbol string, limit int) []BookUpdate
}

// orderBook is the default in-memory implementation of OrderBook.
type orderBook struct {
	broker  string // exchange this book belongs to, e.g. "binance", "alpaca"
	mu      sync.RWMutex
	symbols map[string]*symbolState             // symbol → exchange constraints + history
	pending map[string]map[string]*PendingOrder // accountID → orderID → pending
}

const defaultSymbolHistoryCapacity = 32

type symbolState struct {
	info    SymbolInfo
	history bookUpdateRing
}

type bookUpdateRing struct {
	buf  []BookUpdate
	head int
	size int
}

func newBookUpdateRing(capacity int) bookUpdateRing {
	if capacity <= 0 {
		capacity = defaultSymbolHistoryCapacity
	}
	return bookUpdateRing{
		buf: make([]BookUpdate, capacity),
	}
}

func (r *bookUpdateRing) push(update BookUpdate) {
	if len(r.buf) == 0 {
		return
	}
	r.buf[r.head] = update
	r.head = (r.head + 1) % len(r.buf)
	if r.size < len(r.buf) {
		r.size++
	}
}

func (r *bookUpdateRing) latest() (BookUpdate, bool) {
	if r.size == 0 {
		return BookUpdate{}, false
	}
	idx := (r.head - 1 + len(r.buf)) % len(r.buf)
	return r.buf[idx], true
}

func (r *bookUpdateRing) snapshot(limit int) []BookUpdate {
	if r.size == 0 {
		return nil
	}
	if limit <= 0 || limit > r.size {
		limit = r.size
	}

	out := make([]BookUpdate, 0, limit)
	start := (r.head - r.size + len(r.buf)) % len(r.buf)
	skip := r.size - limit
	for i := 0; i < r.size; i++ {
		idx := (start + i) % len(r.buf)
		if i < skip {
			continue
		}
		out = append(out, r.buf[idx])
	}
	return out
}

// NewOrderBook creates an empty in-memory OrderBook for the given broker.
func NewOrderBook(broker string) OrderBook {
	return &orderBook{
		broker:  broker,
		symbols: make(map[string]*symbolState),
		pending: make(map[string]map[string]*PendingOrder),
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

// RegisterSymbols registers or updates exchange constraints for a symbol set.
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

// Validate checks a proposed order against exchange constraints.
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

	if acctPending, ok := ob.pending[order.OrchestratorID]; ok {
		for _, p := range acctPending {
			if p.Symbol == order.Symbol && p.Side == order.Side {
				return ValidationResult{Valid: false, Reason: fmt.Sprintf("duplicate: pending %s %s order %s", p.Side, p.Symbol, p.OrderID)}
			}
		}
	}

	return ValidationResult{Valid: true, AdjustedQty: adjustedQty}
}

// TrackOrder records a newly placed order as pending.
func (ob *orderBook) TrackOrder(order PendingOrder) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	if ob.pending[order.OrchestratorID] == nil {
		ob.pending[order.OrchestratorID] = make(map[string]*PendingOrder)
	}
	ob.pending[order.OrchestratorID][order.OrderID] = &order
}

// RemoveOrder removes a completed/cancelled order from pending tracking.
func (ob *orderBook) RemoveOrder(accountID, orderID string) {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	if acct, ok := ob.pending[accountID]; ok {
		delete(acct, orderID)
	}
}

// PendingOrders returns all pending orders for an account.
func (ob *orderBook) PendingOrders(accountID string) []PendingOrder {
	ob.mu.RLock()
	defer ob.mu.RUnlock()
	acct := ob.pending[accountID]
	out := make([]PendingOrder, 0, len(acct))
	for _, p := range acct {
		out = append(out, *p)
	}
	return out
}

// SupportedSymbols returns the configured symbol universe for this broker.
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

// RecordUpdate appends a market-data update into the symbol's fixed-cap history buffer.
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

// LatestUpdate returns the most recent market-data update for symbol.
func (ob *orderBook) LatestUpdate(symbol string) (BookUpdate, bool) {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	state, ok := ob.symbols[symbol]
	if !ok {
		return BookUpdate{}, false
	}
	return state.history.latest()
}

// RecentUpdates returns up to limit updates in chronological order.
func (ob *orderBook) RecentUpdates(symbol string, limit int) []BookUpdate {
	ob.mu.RLock()
	defer ob.mu.RUnlock()

	state, ok := ob.symbols[symbol]
	if !ok {
		return nil
	}
	return state.history.snapshot(limit)
}

// ── SpreadTracker ─────────────────────────────────────────────────────────────

const spreadWindow = 20

// SpreadTracker maintains a fixed-size ring buffer of recent bid-ask spreads.
// Useful for stable limit-order pricing and spread anomaly detection.
// Zero heap allocation after init — embed directly in structs.
type SpreadTracker struct {
	buf   [spreadWindow]decimal.Decimal
	head  int
	count int
}

// Push records a new bid/ask observation.
func (s *SpreadTracker) Push(bid, ask decimal.Decimal) {
	s.buf[s.head] = ask.Sub(bid)
	s.head = (s.head + 1) % spreadWindow
	if s.count < spreadWindow {
		s.count++
	}
}

// Avg returns the mean spread across all recorded samples.
// Returns zero if no samples yet.
func (s *SpreadTracker) Avg() decimal.Decimal {
	if s.count == 0 {
		return decimal.Zero
	}
	sum := decimal.Zero
	for i := range s.count {
		sum = sum.Add(s.buf[i])
	}
	return sum.Div(decimal.NewFromInt(int64(s.count)))
}

// Current returns the most recently recorded spread.
func (s *SpreadTracker) Current() decimal.Decimal {
	if s.count == 0 {
		return decimal.Zero
	}
	return s.buf[(s.head-1+spreadWindow)%spreadWindow]
}

// IsAnomalous reports whether the current spread exceeds threshold × average.
// Returns false until the buffer has at least one sample.
func (s *SpreadTracker) IsAnomalous(threshold float64) bool {
	avg := s.Avg()
	return avg.IsPositive() && s.Current().GreaterThan(avg.Mul(decimal.NewFromFloat(threshold)))
}

// ─────────────────────────────────────────────────────────────────────────────

func roundToStep(qty, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return qty
	}
	return qty.Div(step).Floor().Mul(step)
}
