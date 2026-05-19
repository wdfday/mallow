package portfolio

import (
	"time"

	"github.com/shopspring/decimal"
)

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

	switch fill.Side {
	case SideBuy:
		newQty := pos.Qty.Add(fill.Qty)
		if newQty.GreaterThan(epsilon) {
			pos.AvgPrice = pos.AvgPrice.Mul(pos.Qty).Add(fill.Price.Mul(fill.Qty)).Div(newQty)
		}
		if pos.Qty.LessThanOrEqual(decimal.Zero) {
			pos.EntryTimestamp = fill.Timestamp
		}
		pos.Qty = newQty

	case SideSell:
		absQty := pos.Qty.Abs()
		closedQty := fill.Qty
		if fill.Qty.GreaterThan(absQty) {
			closedQty = absQty
		}
		if closedQty.IsPositive() && pos.Qty.IsPositive() {
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
		}
		pos.Qty = pos.Qty.Sub(fill.Qty)
		if pos.Qty.Abs().LessThan(epsilon) {
			delete(p.positions, fill.Symbol)
			return
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
func (p *Portfolio) ApplySync(cash decimal.Decimal, positions []SyncedPosition) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cash = cash
	p.positions = make(map[string]*Position, len(positions))
	for _, sp := range positions {
		mv := sp.Qty.Mul(sp.CurPrice)
		p.positions[sp.Symbol] = &Position{
			Symbol:        sp.Symbol,
			Qty:           sp.Qty,
			AvgPrice:      sp.AvgPrice,
			CurrentPrice:  sp.CurPrice,
			UnrealizedPnL: sp.CurPrice.Sub(sp.AvgPrice).Mul(sp.Qty),
			MarketValue:   mv,
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
func (p *Portfolio) RestorePosition(symbol, side string, qty, avgPrice, currentPrice decimal.Decimal) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if side == "sell" {
		qty = qty.Neg() // short position has negative Qty
	}
	mv := qty.Mul(currentPrice)
	p.positions[symbol] = &Position{
		Symbol:        symbol,
		Qty:           qty,
		AvgPrice:      avgPrice,
		CurrentPrice:  currentPrice,
		UnrealizedPnL: currentPrice.Sub(avgPrice).Mul(qty),
		MarketValue:   mv,
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
	})
	if eq.GreaterThan(p.peakEquity) {
		p.peakEquity = eq
	}
}
