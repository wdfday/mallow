package portfolio

import (
	"time"

	"github.com/shopspring/decimal"
)

// TotalReturn returns the total return as a fraction (e.g. 0.05 = 5%).
func (p *Portfolio) TotalReturn() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.initialCapital.IsZero() {
		return 0
	}
	eq := p.equityLocked()
	f, _ := eq.Sub(p.initialCapital).Div(p.initialCapital).Float64()
	return f
}

// CurrentDrawdown returns the current drawdown from peak equity as a fraction.
func (p *Portfolio) CurrentDrawdown() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.peakEquity.IsZero() {
		return 0
	}
	eq := p.equityLocked()
	f, _ := p.peakEquity.Sub(eq).Div(p.peakEquity).Float64()
	return f
}

// WinRate returns the fraction of trades that closed with positive PnL.
func (p *Portfolio) WinRate() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.winRateLocked()
}

// DailyPnL returns the sum of PnL for all trades closed today (UTC).
func (p *Portfolio) DailyPnL() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dailyPnLLocked()
}

// ---------------------------------------------------------------------------
// Locked helpers — caller must hold at least a read lock.
// ---------------------------------------------------------------------------

func (p *Portfolio) winRateLocked() float64 {
	if len(p.trades) == 0 {
		return 0
	}
	wins := 0
	for _, t := range p.trades {
		if t.PnL.IsPositive() {
			wins++
		}
	}
	return float64(wins) / float64(len(p.trades))
}

func (p *Portfolio) dailyPnLLocked() decimal.Decimal {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	pnl := decimal.Zero
	for _, t := range p.trades {
		if t.ExitTimestamp.After(today) {
			pnl = pnl.Add(t.PnL)
		}
	}
	return pnl
}
