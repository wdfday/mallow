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
	defer func() {
		eq := p.equityLocked()
		if eq.GreaterThan(p.peakEquity) {
			p.peakEquity = eq
		}
	}()
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
			pos.EntryCommission = fill.Commission
		} else {
			// Phantom sell: exchange fill arrived while portfolio is flat (Sync
			// cleared the position, or out-of-order WS delivery). Cash was already
			// credited above. Record a transient negative Qty so a subsequent BUY
			// fill can cancel it cleanly; helmSnapshot / equityLocked skip negative
			// positions so it does not inflate equity or appear in the UI.
			pos.Qty = fill.Qty.Neg()
			pos.EntryCommission = fill.Commission
		}
		pos.AvgPrice = fill.Price
	} else if pos.Qty.IsPositive() {
		// Existing long position.
		if fill.Side == SideBuy {
			// Adding to long.
			newQty := pos.Qty.Add(fill.Qty)
			pos.AvgPrice = pos.AvgPrice.Mul(pos.Qty).Add(fill.Price.Mul(fill.Qty)).Div(newQty)
			pos.Qty = newQty
			pos.EntryCommission = pos.EntryCommission.Add(fill.Commission)
		} else {
			// Reducing or fully closing long.
			// Use clamped qty for PnL so we never record profit on qty we didn't hold.
			closedQty := minDecimal(fill.Qty, pos.Qty)
			var entryComm decimal.Decimal
			if pos.Qty.IsPositive() {
				entryComm = pos.EntryCommission.Mul(closedQty).Div(pos.Qty)
			}
			pnl := fill.Price.Sub(pos.AvgPrice).Mul(closedQty).Sub(entryComm).Sub(fill.Commission)
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

			pos.EntryCommission = pos.EntryCommission.Sub(entryComm)
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

// ApplySync replaces portfolio state wholesale with authoritative data from
// the exchange REST API. Called on helm create, enable, and every SYNC_INTERVAL.
//
// It does NOT touch trades or peakEquity — peakEquity advances only via fills
// (ApplyFill) to prevent broker-sync moments with temporarily inflated unrealized
// gains from setting a false peak.
// SyncCash overrides the portfolio's cash balance with the value reported by
// the exchange. Called on balance-push WS events so the portfolio converges to
// the broker's authoritative free balance (which includes fees not tracked locally).
func (p *Portfolio) SyncCash(v decimal.Decimal) {
	p.mu.Lock()
	p.cash = v
	p.mu.Unlock()
}

// SyncBalance upserts a single asset's live free-balance into the read-only
// balances side-channel (see Balances accessor) — does not touch cash,
// equity, or sizing. Called on every balance-push WS event, for every asset
// reported (including the quote asset, which SyncCash separately keeps
// authoritative for the trading hot path). Only Free is refreshed; Locked/
// MarginBalance/UnrealizedPnL keep whatever the last REST ApplySync set,
// since a balance-push event doesn't carry those.
func (p *Portfolio) SyncBalance(asset string, free decimal.Decimal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.balances {
		if p.balances[i].Asset == asset {
			p.balances[i].Free = free
			return
		}
	}
	p.balances = append(p.balances, Balance{Asset: asset, Free: free})
}

// SyncLivePosition reconciles a symbol's position with an authoritative WS
// position push from the exchange. A non-zero qty upserts Qty/AvgPrice/
// UnrealizedPnL, creating the position if it didn't exist locally (e.g. an
// externally-opened position becomes visible immediately instead of waiting
// for the next REST sync). CurrentPrice/MarketValue are left untouched —
// still driven by UpdatePrice from live market ticks, not by this push.
//
// A zero qty is deliberately NOT treated as "close the position": formally
// closing a position is the job of the existing audit-tracked paths (poslog
// KindPositionClosed, the startup reconciler's external-close handling),
// which create a proper Trade record and notify the owning hand. Silently
// deleting the entry here would desync Portfolio from the Hand's own
// position bookkeeping with no audit trail. Returns true if qty was zero, so
// the caller can log it instead.
func (p *Portfolio) SyncLivePosition(symbol string, qty, entryPrice, unrealizedPnL decimal.Decimal, at time.Time) (skippedZero bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if qty.IsZero() {
		return true
	}
	pos, exists := p.positions[symbol]
	if !exists {
		pos = &Position{Symbol: symbol, EntryTimestamp: at}
		p.positions[symbol] = pos
	}
	pos.Qty = qty
	pos.AvgPrice = entryPrice
	pos.UnrealizedPnL = unrealizedPnL
	return false
}

// ApplySync replaces portfolio state wholesale with authoritative data from
// the exchange REST API. Called on helm create, enable, and every SYNC_INTERVAL.
//
// exchangeEquity, balances, and marginRatio are exchange-reported extras
// (AccountSnapshot.AccountEquity/Balances/MarginRatio) — stored as read-only
// snapshots (see ExchangeEquity/Balances/MarginRatio accessors) and never fed
// into the live cash/position-driven equityLocked(). Pass decimal.Zero / nil
// for exchanges that don't report them (e.g. Binance spot has no margin
// concept); the zero value is a legitimate "not reported" for these fields
// since callers only ever read them through the accessors, never assume
// non-zero.
func (p *Portfolio) ApplySync(cash decimal.Decimal, positions []SyncedPosition, exchangeEquity decimal.Decimal, balances []Balance, marginRatio decimal.Decimal) {
	p.mu.Lock()
	defer p.mu.Unlock()

	oldPositions := p.positions
	p.cash = cash
	p.exchangeEquity = exchangeEquity
	p.balances = balances
	p.marginRatio = marginRatio
	p.positions = make(map[string]*Position, len(positions))
	for _, sp := range positions {
		// Preserve known avg_price when the exchange returns zero.
		// Binance spot REST does not return avg entry price — it returns "" / "0".
		// Using 0 would make UnrealizedPnL = CurPrice*Qty (full market value), not
		// the actual PnL from entry. Keep the in-memory value if we have one.
		avgPrice := sp.AvgPrice
		entryTS := time.Time{}
		var entryComm decimal.Decimal
		if old, ok := oldPositions[sp.Symbol]; ok {
			entryTS = old.EntryTimestamp
			entryComm = old.EntryCommission
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
			Symbol:           sp.Symbol,
			Qty:              sp.Qty,
			AvgPrice:         avgPrice,
			CurrentPrice:     sp.CurPrice,
			UnrealizedPnL:    unrealized,
			MarketValue:      sp.Qty.Mul(sp.CurPrice),
			EntryTimestamp:   entryTS,
			EntryCommission:  entryComm,
			Side:             sp.Side,
			Leverage:         sp.Leverage,
			LiquidationPrice: sp.LiquidationPrice,
			MarginMode:       sp.MarginMode,
		}
	}
	// First-sync initialisation: seed peakEquity from synced equity if no fills yet.
	if p.peakEquity.IsZero() {
		p.peakEquity = p.equityLocked()
	}
}

// RestorePosition injects a single position into the portfolio without touching
// cash. Called by the poslog reconciler on startup to replay positions that were
// open when the process last stopped. avgPrice is the original entry price;
// currentPrice is the latest known market price.
func (p *Portfolio) RestorePosition(symbol, side string, qty, avgPrice, currentPrice, deployedCapital decimal.Decimal, entryTimestamp time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if side == "sell" {
		qty = qty.Neg() // short position has negative Qty
	}
	mv := qty.Mul(currentPrice)

	// Derive entry commission from deployed capital.
	entryComm := decimal.Zero
	if side == "buy" && deployedCapital.IsPositive() {
		entryComm = deployedCapital.Sub(qty.Mul(avgPrice))
		if entryComm.IsNegative() {
			entryComm = decimal.Zero
		}
	}

	p.positions[symbol] = &Position{
		Symbol:          symbol,
		Qty:             qty,
		AvgPrice:        avgPrice,
		CurrentPrice:    currentPrice,
		UnrealizedPnL:   currentPrice.Sub(avgPrice).Mul(qty),
		MarketValue:     mv,
		EntryTimestamp:  entryTimestamp,
		EntryCommission: entryComm,
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
