package portfolio

import (
	"time"

	"github.com/shopspring/decimal"
)

func minDecimal(a, b decimal.Decimal) decimal.Decimal {
	if a.LessThan(b) {
		return a
	}
	return b
}

// ApplyFill processes a confirmed order fill: adjusts cash, updates the open
// position, and records a completed Trade when a long position is fully closed.
//
// Cash impact:
//   - Buy:  cash -= qty*price + commission
//   - Sell: cash += qty*price - commission
//
// Position sign convention: Qty > 0 = long, Qty < 0 = short.
// A sell that brings Qty to zero removes the position from the map.
func (p *Portfolio) ApplyFill(fill Fill) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Cash impact.
	gross := fill.Qty.Mul(fill.Price)
	switch fill.Side {
	case SideBuy:
		p.cash = p.cash.Sub(gross.Add(fill.Commission))
	case SideSell:
		p.cash = p.cash.Add(gross.Sub(fill.Commission))
	}

	pos, exists := p.positions[fill.Symbol]
	if !exists {
		pos = &Position{
			Symbol:         fill.Symbol,
			EntryTimestamp: fill.Timestamp,
		}
		p.positions[fill.Symbol] = pos
	}

	epsilon := decimal.NewFromFloat(1e-9)

	if pos.Qty.IsZero() {
		// New position opening or phantom fill on flat portfolio.
		pos.EntryTimestamp = fill.Timestamp
		if fill.Side == SideBuy {
			pos.Qty = fill.Qty
		} else {
			// Phantom sell: exchange fill arrived while portfolio is flat (Sync
			// cleared the position, or out-of-order WS delivery). Cash was already
			// credited above. Record a transient negative Qty so a subsequent BUY
			// fill can cancel it cleanly; helmSnapshot / equityLocked skip negative
			// positions so it does not inflate equity or appear in the UI.
			pos.Qty = fill.Qty.Neg()
		}
		pos.AvgPrice = fill.Price
	} else if pos.Qty.IsPositive() {
		// Existing long position.
		if fill.Side == SideBuy {
			// Adding to long.
			newQty := pos.Qty.Add(fill.Qty)
			pos.AvgPrice = pos.AvgPrice.Mul(pos.Qty).Add(fill.Price.Mul(fill.Qty)).Div(newQty)
			pos.Qty = newQty
		} else {
			// Reducing or fully closing long.
			// Use clamped qty for PnL so we never record profit on qty we didn't hold.
			closedQty := minDecimal(fill.Qty, pos.Qty)
			pnl := fill.Price.Sub(pos.AvgPrice).Mul(closedQty).Sub(fill.Commission)
			costBasis := pos.AvgPrice.Mul(closedQty)
			var pnlPct decimal.Decimal
			if costBasis.IsPositive() {
				pnlPct = pnl.Div(costBasis)
			}
			p.trades = append(p.trades, Trade{
				HandID:         fill.HandID,
				Symbol:         fill.Symbol,
				Side:           SideBuy,
				Qty:            closedQty,
				EntryPrice:     pos.AvgPrice,
				ExitPrice:      fill.Price,
				EntryTimestamp: pos.EntryTimestamp,
				ExitTimestamp:  fill.Timestamp,
				PnL:            pnl,
				PnLPct:         pnlPct,
			})

			// Subtract full fill.Qty so slight oversell goes negative rather than
			// leaving a dust long. helmSnapshot ignores negative Qty positions.
			pos.Qty = pos.Qty.Sub(fill.Qty)
			if pos.Qty.Abs().LessThan(epsilon) || pos.Qty.IsNegative() {
				delete(p.positions, fill.Symbol)
				return
			}
		}
	} else {
		// Negative Qty — transient phantom short waiting for a covering BUY.
		if fill.Side == SideBuy {
			// Covering the phantom short.
			pos.Qty = pos.Qty.Add(fill.Qty)
			if pos.Qty.Abs().LessThan(epsilon) || !pos.Qty.IsNegative() {
				// Fully covered (or overshot to long): delete; the
				// overshoot becomes net zero — no position or a flat entry.
				delete(p.positions, fill.Symbol)
				return
			}
		} else {
			// More sells while already negative: deepen the phantom short.
			pos.Qty = pos.Qty.Sub(fill.Qty)
		}
	}

	pos.CurrentPrice = fill.Price
	if pos.AvgPrice.IsZero() {
		pos.UnrealizedPnL = decimal.Zero
	} else {
		pos.UnrealizedPnL = pos.CurrentPrice.Sub(pos.AvgPrice).Mul(pos.Qty)
	}
	pos.MarketValue = pos.Qty.Mul(pos.CurrentPrice)
}

// UpdatePrice updates the current market price for a symbol and recalculates
// unrealized PnL and market value. No-op if the symbol has no open position.
func (p *Portfolio) UpdatePrice(symbol string, price decimal.Decimal) {
	p.mu.Lock()
	defer p.mu.Unlock()

	pos, ok := p.positions[symbol]
	if !ok {
		return
	}
	pos.CurrentPrice = price
	// avg_price=0 means no fill event (e.g. paper account pre-seeded balance) —
	// unrealized PnL is meaningless, suppress it.
	if pos.AvgPrice.IsZero() {
		pos.UnrealizedPnL = decimal.Zero
	} else {
		pos.UnrealizedPnL = price.Sub(pos.AvgPrice).Mul(pos.Qty)
	}
	pos.MarketValue = pos.Qty.Mul(price)
}

// ApplySync replaces portfolio state wholesale with authoritative data from
// the exchange REST API. Called on helm create, enable, and every SYNC_INTERVAL.
//
// It does NOT touch equityCurve, trades, or peakEquity — those are updated
// only via fills (ApplyFill / RecordEquity) to prevent broker-sync moments
// with temporarily inflated unrealized gains from setting a false peak.
// SyncCash overrides the portfolio's cash balance with the value reported by
// the exchange. Called on balance-push WS events so the portfolio converges to
// the broker's authoritative free balance (which includes fees not tracked locally).
func (p *Portfolio) SyncCash(v decimal.Decimal) {
	p.mu.Lock()
	p.cash = v
	p.mu.Unlock()
}

func (p *Portfolio) ApplySync(cash decimal.Decimal, positions []SyncedPosition) {
	p.mu.Lock()
	defer p.mu.Unlock()

	oldPositions := p.positions
	p.cash = cash
	p.positions = make(map[string]*Position, len(positions))
	for _, sp := range positions {
		// Preserve known avg_price when the exchange returns zero.
		// Binance spot REST does not return avg entry price — it returns "" / "0".
		// Using 0 would make UnrealizedPnL = CurPrice*Qty (full market value), not
		// the actual PnL from entry. Keep the in-memory value if we have one.
		avgPrice := sp.AvgPrice
		entryTS := time.Time{}
		if old, ok := oldPositions[sp.Symbol]; ok {
			entryTS = old.EntryTimestamp
			if avgPrice.IsZero() && old.AvgPrice.IsPositive() {
				avgPrice = old.AvgPrice
			}
		}

		// Unrealized PnL is only meaningful when avg_price is known.
		var unrealized decimal.Decimal
		if avgPrice.IsPositive() {
			unrealized = sp.CurPrice.Sub(avgPrice).Mul(sp.Qty)
		}

		p.positions[sp.Symbol] = &Position{
			Symbol:         sp.Symbol,
			Qty:            sp.Qty,
			AvgPrice:       avgPrice,
			CurrentPrice:   sp.CurPrice,
			UnrealizedPnL:  unrealized,
			MarketValue:    sp.Qty.Mul(sp.CurPrice),
			EntryTimestamp: entryTS,
		}
	}
	// NOTE: peakEquity is intentionally NOT updated here. Broker syncs can happen
	// at moments when unrealized gains are temporarily high, creating an inflated
	// high-water mark that makes subsequent drawdown measurements wrong.
	// peakEquity advances only via fills: ApplyFill → RecordEquity (helm_trading.go).
	//
	// First-sync initialisation: if no fills have been recorded yet (peakEquity == 0),
	// seed the peak from the synced equity so the drawdown gate has a sensible baseline.
	if p.peakEquity.IsZero() {
		p.peakEquity = p.equityLocked()
	}
}

// RestorePosition injects a single position into the portfolio without touching
// cash. Called by the poslog reconciler on startup to replay positions that were
// open when the process last stopped. avgPrice is the original entry price;
// currentPrice is the latest known market price.
func (p *Portfolio) RestorePosition(symbol, side string, qty, avgPrice, currentPrice decimal.Decimal, entryTimestamp time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if side == "sell" {
		qty = qty.Neg() // short position has negative Qty
	}
	mv := qty.Mul(currentPrice)
	p.positions[symbol] = &Position{
		Symbol:         symbol,
		Qty:            qty,
		AvgPrice:       avgPrice,
		CurrentPrice:   currentPrice,
		UnrealizedPnL:  currentPrice.Sub(avgPrice).Mul(qty),
		MarketValue:    mv,
		EntryTimestamp: entryTimestamp,
	}
}

// RecordEquity appends a timestamped equity snapshot to the equity curve.
// Also updates peakEquity if the current equity exceeds it.
func (p *Portfolio) RecordEquity(ts time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	eq := p.equityLocked()
	p.equityCurve = append(p.equityCurve, EquityPoint{
		Timestamp: ts,
		Equity:    eq,
		Cash:      p.cash,
	})
	if eq.GreaterThan(p.peakEquity) {
		p.peakEquity = eq
	}
}

// UpdatePeakEquity advances peakEquity to the current equity if it is higher.
// Useful when a caller needs an on-demand peak refresh outside the normal
// ApplySync / RecordEquity cadence.
func (p *Portfolio) UpdatePeakEquity() {
	p.mu.Lock()
	defer p.mu.Unlock()
	eq := p.equityLocked()
	if eq.GreaterThan(p.peakEquity) {
		p.peakEquity = eq
	}
}

// ResetPeakToCurrentEquity redefines the high-water mark as the current equity.
// Called by the risk Manager on ResetHalt so that the max-drawdown gate
// measures loss from the post-reset level rather than the old peak — preventing
// an immediate re-halt when the user tries to trade after acknowledging a loss.
func (p *Portfolio) ResetPeakToCurrentEquity() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.peakEquity = p.equityLocked()
}

// RemovePosition completely deletes the open position for the given symbol from the portfolio.
// Typically called when a position is released/orphaned, so the portfolio stops tracking it.
func (p *Portfolio) RemovePosition(symbol string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.positions, symbol)
}
