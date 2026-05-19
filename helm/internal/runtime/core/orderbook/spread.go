package orderbook

import "github.com/shopspring/decimal"

const spreadWindow = 20

// SpreadTracker maintains a fixed-size ring buffer of recent bid-ask spreads.
// Useful for stable limit-order pricing and spread anomaly detection.
// Zero heap allocation after init — safe to embed directly in structs.
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
// Returns zero if no samples have been pushed yet.
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
// Returns false until at least one sample has been pushed.
func (s *SpreadTracker) IsAnomalous(threshold float64) bool {
	avg := s.Avg()
	return avg.IsPositive() && s.Current().GreaterThan(avg.Mul(decimal.NewFromFloat(threshold)))
}
