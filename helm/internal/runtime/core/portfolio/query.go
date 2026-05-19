package portfolio

import "github.com/shopspring/decimal"

// Cash returns the current cash balance.
func (p *Portfolio) Cash() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cash
}

// Equity returns total equity: cash + Σ(position.Qty × position.CurrentPrice).
func (p *Portfolio) Equity() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.equityLocked()
}

// Positions returns a snapshot of all open positions.
func (p *Portfolio) Positions() []Position {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]Position, 0, len(p.positions))
	for _, pos := range p.positions {
		result = append(result, *pos)
	}
	return result
}

// GetPosition returns a copy of the position for symbol, or nil if flat.
func (p *Portfolio) GetPosition(symbol string) *Position {
	p.mu.RLock()
	defer p.mu.RUnlock()

	pos, ok := p.positions[symbol]
	if !ok {
		return nil
	}
	cp := *pos
	return &cp
}

// Trades returns a snapshot of all completed round-trip trades.
func (p *Portfolio) Trades() []Trade {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]Trade, len(p.trades))
	copy(result, p.trades)
	return result
}

// EquityCurve returns a snapshot of the recorded equity curve.
func (p *Portfolio) EquityCurve() []EquityPoint {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]EquityPoint, len(p.equityCurve))
	copy(result, p.equityCurve)
	return result
}

// Summary returns a full portfolio snapshot for API responses.
func (p *Portfolio) Summary() Summary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	positions := make([]Position, 0, len(p.positions))
	for _, pos := range p.positions {
		positions = append(positions, *pos)
	}

	eq := p.equityLocked()
	dd := 0.0
	if p.peakEquity.IsPositive() {
		v, _ := p.peakEquity.Sub(eq).Div(p.peakEquity).Mul(decimal.NewFromInt(100)).Float64()
		dd = v
	}
	totalReturn := 0.0
	if p.initialCapital.IsPositive() {
		v, _ := eq.Sub(p.initialCapital).Div(p.initialCapital).Mul(decimal.NewFromInt(100)).Float64()
		totalReturn = v
	}

	return Summary{
		InitialCapital: p.initialCapital,
		Cash:           p.cash,
		Equity:         eq,
		TotalReturn:    totalReturn,
		CurrentDD:      dd,
		MaxDD:          p.maxDrawdownLocked() * 100,
		WinRate:        p.winRateLocked() * 100,
		TotalTrades:    len(p.trades),
		OpenPositions:  len(p.positions),
		DailyPnL:       p.dailyPnLLocked(),
		Positions:      positions,
	}
}
