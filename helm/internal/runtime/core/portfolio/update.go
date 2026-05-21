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
		// New position opening.
		pos.EntryTimestamp = fill.Timestamp
		if fill.Side == SideBuy {
			pos.Qty = fill.Qty
		} else {
			pos.Qty = fill.Qty.Neg()
		}
		pos.AvgPrice = fill.Price
	} else if pos.Qty.IsPositive() {
		// Existing Long position.
		if fill.Side == SideBuy {
			// Adding to long.
			newQty := pos.Qty.Add(fill.Qty)
			pos.AvgPrice = pos.AvgPrice.Mul(pos.Qty).Add(fill.Price.Mul(fill.Qty)).Div(newQty)
			pos.Qty = newQty
		} else {
			// Reducing, closing, or reversing long to short.
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

			pos.Qty = pos.Qty.Sub(fill.Qty)
			if pos.Qty.LessThan(epsilon.Neg()) {
				// Reversed to short.
				pos.AvgPrice = fill.Price
				pos.EntryTimestamp = fill.Timestamp
			} else if pos.Qty.Abs().LessThan(epsilon) {
				delete(p.positions, fill.Symbol)
				return
			}
		}
	} else {
		// Existing Short position (pos.Qty is negative).
		if fill.Side == SideSell {
			// Adding to short.
			absQty := pos.Qty.Abs()
			newQty := pos.Qty.Sub(fill.Qty)
			pos.AvgPrice = pos.AvgPrice.Mul(absQty).Add(fill.Price.Mul(fill.Qty)).Div(newQty.Abs())
			pos.Qty = newQty
		} else {
			// Reducing, closing, or reversing short to long.
			absQty := pos.Qty.Abs()
			closedQty := minDecimal(fill.Qty, absQty)
			pnl := pos.AvgPrice.Sub(fill.Price).Mul(closedQty).Sub(fill.Commission)
			costBasis := pos.AvgPrice.Mul(closedQty)
			var pnlPct decimal.Decimal
			if costBasis.IsPositive() {
				pnlPct = pnl.Div(costBasis)
			}
			p.trades = append(p.trades, Trade{
				HandID:         fill.HandID,
				Symbol:         fill.Symbol,
				Side:           SideSell,
				Qty:            closedQty,
				EntryPrice:     pos.AvgPrice,
				ExitPrice:      fill.Price,
				EntryTimestamp: pos.EntryTimestamp,
				ExitTimestamp:  fill.Timestamp,
				PnL:            pnl,
				PnLPct:         pnlPct,
			})

			pos.Qty = pos.Qty.Add(fill.Qty)
			if pos.Qty.GreaterThan(epsilon) {
				// Reversed to long.
				pos.AvgPrice = fill.Price
				pos.EntryTimestamp = fill.Timestamp
			} else if pos.Qty.Abs().LessThan(epsilon) {
				delete(p.positions, fill.Symbol)
				return
			}
		}
	}

	pos.CurrentPrice = fill.Price
	pos.UnrealizedPnL = pos.CurrentPrice.Sub(pos.AvgPrice).Mul(pos.Qty)
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
	pos.UnrealizedPnL = price.Sub(pos.AvgPrice).Mul(pos.Qty)
	pos.MarketValue = pos.Qty.Mul(price)
}

// ApplySync replaces portfolio state wholesale with authoritative data from
// the exchange REST API. Called on helm create, enable, and every SYNC_INTERVAL.
//
// It does NOT touch equityCurve or trades — those are append-only history.
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
		mv := sp.Qty.Mul(sp.CurPrice)
		entryTS := time.Time{}
		if old, ok := oldPositions[sp.Symbol]; ok {
			entryTS = old.EntryTimestamp
		}
		p.positions[sp.Symbol] = &Position{
			Symbol:         sp.Symbol,
			Qty:            sp.Qty,
			AvgPrice:       sp.AvgPrice,
			CurrentPrice:   sp.CurPrice,
			UnrealizedPnL:  sp.CurPrice.Sub(sp.AvgPrice).Mul(sp.Qty),
			MarketValue:    mv,
			EntryTimestamp: entryTS,
		}
	}
	eq := p.equityLocked()
	if eq.GreaterThan(p.peakEquity) {
		p.peakEquity = eq
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

// RemovePosition completely deletes the open position for the given symbol from the portfolio.
// Typically called when a position is released/orphaned, so the portfolio stops tracking it.
func (p *Portfolio) RemovePosition(symbol string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.positions, symbol)
}
