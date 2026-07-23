package portfolio

import "github.com/shopspring/decimal"

// RealizedPnL returns the sum of PnL from all completed round-trip trades.
func (p *Portfolio) RealizedPnL() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	total := decimal.Zero
	for i := range p.trades {
		total = total.Add(p.trades[i].PnL)
	}
	return total
}

// UnrealizedPnL returns the mark-to-market PnL of all currently open positions,
// as of each position's CurrentPrice (refreshed by ApplyFill/ApplySync — see doc.go).
func (p *Portfolio) UnrealizedPnL() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	total := decimal.Zero
	for _, pos := range p.positions {
		total = total.Add(pos.UnrealizedPnL)
	}
	return total
}

// Cash returns the current cash balance.
func (p *Portfolio) Cash() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cash
}

// Equity returns total equity: cash + Σ(position.Qty × position.CurrentPrice).
// This is the live, continuously-updating value — see ExchangeEquity for the
// exchange's own last-synced margin-adjusted figure.
func (p *Portfolio) Equity() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.equityLocked()
}

// ExchangeEquity returns the exchange's own margin-adjusted total equity as of
// the last ApplySync (OKX totalEq, fbinance TotalMarginBalance, Alpaca Equity).
// Zero if the exchange doesn't report one (e.g. Binance spot) or no sync has
// happened yet. This is a snapshot, not live — prefer Equity() for anything
// that needs to react to price moves between syncs.
func (p *Portfolio) ExchangeEquity() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exchangeEquity
}

// MarginRatio returns the exchange-reported account-level maintenance margin
// ratio as of the last ApplySync (approaches 1.0 near liquidation). Zero if
// not applicable/reported — callers (e.g. the risk manager's margin-ratio
// gate) must treat zero as "no data", not "no risk".
func (p *Portfolio) MarginRatio() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.marginRatio
}

// Balances returns the per-asset balances as last reported by the exchange
// REST sync — informational only; trading logic keys off Cash()/Positions(),
// not this. Empty if the exchange doesn't report per-asset balances (Alpaca).
func (p *Portfolio) Balances() []Balance {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Balance, len(p.balances))
	copy(out, p.balances)
	return out
}

// GrossExposure returns the total open notional across all positions, |Qty| × CurrentPrice
// (longs and shorts both add to exposure). Used by the risk manager's account-level
// exposure ceiling. Phantom shorts (transient negative-Qty bookkeeping) are excluded.
func (p *Portfolio) GrossExposure() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	gross := decimal.Zero
	for _, pos := range p.positions {
		if pos.Qty.IsPositive() {
			gross = gross.Add(pos.Qty.Mul(pos.CurrentPrice))
		}
	}
	return gross
}

// Positions returns a snapshot of all open long positions.
// Negative-Qty entries (transient phantom shorts absorbing out-of-order fills)
// are excluded — they are bookkeeping state, not real open exposure.
func (p *Portfolio) Positions() []Position {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]Position, 0, len(p.positions))
	for _, pos := range p.positions {
		if pos.Qty.IsPositive() {
			result = append(result, *pos)
		}
	}
	return result
}

// GetPosition returns a copy of the position for symbol, or nil if flat.
func (p *Portfolio) GetPosition(symbol string) *Position {
	p.mu.RLock()
	defer p.mu.RUnlock()

	pos, ok := p.positions[symbol]
	if !ok || !pos.Qty.IsPositive() {
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

// Summary returns a full portfolio snapshot for API responses.
func (p *Portfolio) Summary() Summary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	positions := make([]Position, 0, len(p.positions))
	for _, pos := range p.positions {
		if pos.Qty.IsPositive() {
			positions = append(positions, *pos)
		}
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

	// Derived aggregates over current positions.
	// Negative-Qty entries are transient phantom shorts — exclude from all aggregates.
	deployed := decimal.Zero
	unrealized := decimal.Zero
	for _, pos := range p.positions {
		if !pos.Qty.IsPositive() {
			continue
		}
		if pos.AvgPrice.IsPositive() {
			deployed = deployed.Add(pos.Qty.Mul(pos.AvgPrice))
		}
		unrealized = unrealized.Add(pos.UnrealizedPnL)
	}
	// Realized PnL = Σ closed-trade PnL (since helm hydrate / restart).
	realized := decimal.Zero
	for i := range p.trades {
		realized = realized.Add(p.trades[i].PnL)
	}

	return Summary{
		InitialCapital:  p.initialCapital,
		Cash:            p.cash,
		AvailableCash:   p.cash, // deprecated alias
		Equity:          eq,
		DeployedCapital: deployed,
		UnrealizedPnL:   unrealized,
		RealizedPnL:     realized,
		TotalReturn:     totalReturn,
		CurrentDD:       dd,
		MaxDD:           0,
		WinRate:         p.winRateLocked() * 100,
		TotalTrades:     len(p.trades),
		OpenPositions:   len(positions),
		DailyPnL:        p.dailyPnLLocked(),
		Positions:       positions,
	}
}
