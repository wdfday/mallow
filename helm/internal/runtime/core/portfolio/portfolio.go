package portfolio

import (
	"sync"

	"github.com/shopspring/decimal"
)

// Portfolio tracks cash, open positions, completed trades, and equity curve
// for a single broker account. Thread-safe via an internal RWMutex.
//
// See doc.go for a full description of responsibilities and update paths.
type Portfolio struct {
	mu             sync.RWMutex
	initialCapital decimal.Decimal
	cash           decimal.Decimal
	positions      map[string]*Position
	trades         []Trade
	peakEquity     decimal.Decimal
}

// New creates a Portfolio with the given initial capital.
func New(initialCapital decimal.Decimal) *Portfolio {
	return &Portfolio{
		initialCapital: initialCapital,
		cash:           initialCapital,
		positions:      make(map[string]*Position),
		trades:         make([]Trade, 0, 256),
		peakEquity:     initialCapital,
	}
}

// equityLocked computes equity without acquiring the lock.
// Caller must hold at least p.mu.RLock.
//
// Only long (positive Qty) positions contribute to equity. Negative Qty entries
// are transient phantom shorts used to absorb out-of-order WS fills; they should
// not reduce equity below cash (proceeds were already credited to cash).
func (p *Portfolio) equityLocked() decimal.Decimal {
	mv := decimal.Zero
	for _, pos := range p.positions {
		if pos.Qty.IsPositive() {
			mv = mv.Add(pos.Qty.Mul(pos.CurrentPrice))
		}
	}
	return p.cash.Add(mv)
}
