package portfolio

import (
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// Side represents the direction of a trade.
type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

// Position represents an open position for one symbol.
type Position struct {
	Symbol         string          `json:"symbol"`
	Qty            decimal.Decimal `json:"qty"` // positive = long, negative = short
	AvgPrice       decimal.Decimal `json:"avg_price"`
	CurrentPrice   decimal.Decimal `json:"current_price"`
	UnrealizedPnL  decimal.Decimal `json:"unrealized_pnl"`
	MarketValue    decimal.Decimal `json:"market_value"`
	EntryTimestamp time.Time       `json:"entry_timestamp"`
}

// Fill represents a confirmed order execution.
type Fill struct {
	Timestamp  time.Time       `json:"timestamp"`
	Symbol     string          `json:"symbol"`
	Side       Side            `json:"side"`
	Qty        decimal.Decimal `json:"qty"`
	Price      decimal.Decimal `json:"price"`
	Commission decimal.Decimal `json:"commission"`
}

// Trade represents a completed round-trip trade (entry + exit).
type Trade struct {
	Symbol         string          `json:"symbol"`
	Side           Side            `json:"side"`
	Qty            decimal.Decimal `json:"qty"`
	EntryPrice     decimal.Decimal `json:"entry_price"`
	ExitPrice      decimal.Decimal `json:"exit_price"`
	EntryTimestamp time.Time       `json:"entry_timestamp"`
	ExitTimestamp  time.Time       `json:"exit_timestamp"`
	PnL            decimal.Decimal `json:"pnl"`
	PnLPct         decimal.Decimal `json:"pnl_pct"`
}

// EquityPoint is a snapshot of total equity at a point in time.
type EquityPoint struct {
	Timestamp time.Time       `json:"timestamp"`
	Equity    decimal.Decimal `json:"equity"`
}

// Portfolio tracks cash, positions, equity curve, and completed trades.
// Thread-safe for concurrent access from signal handler and API.
type Portfolio struct {
	mu             sync.RWMutex
	initialCapital decimal.Decimal
	cash           decimal.Decimal
	positions      map[string]*Position
	equityCurve    []EquityPoint
	trades         []Trade
	peakEquity     decimal.Decimal
}

// New creates a Portfolio with the given initial capital.
func New(initialCapital decimal.Decimal) *Portfolio {
	return &Portfolio{
		initialCapital: initialCapital,
		cash:           initialCapital,
		positions:      make(map[string]*Position),
		equityCurve:    make([]EquityPoint, 0, 1024),
		trades:         make([]Trade, 0, 256),
		peakEquity:     initialCapital,
	}
}

// Cash returns the current cash balance.
func (p *Portfolio) Cash() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cash
}

// Equity returns total equity = cash + sum of market values.
func (p *Portfolio) Equity() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.equityLocked()
}

func (p *Portfolio) equityLocked() decimal.Decimal {
	mv := decimal.Zero
	for _, pos := range p.positions {
		mv = mv.Add(pos.Qty.Mul(pos.CurrentPrice))
	}
	return p.cash.Add(mv)
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

// GetPosition returns the position for a symbol, or nil if none.
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

// Trades returns a snapshot of all completed trades.
func (p *Portfolio) Trades() []Trade {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]Trade, len(p.trades))
	copy(result, p.trades)
	return result
}

// EquityCurve returns a snapshot of the equity curve.
func (p *Portfolio) EquityCurve() []EquityPoint {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]EquityPoint, len(p.equityCurve))
	copy(result, p.equityCurve)
	return result
}

// SyncedPosition is a position as received from an external sync source (exchange REST API).
type SyncedPosition struct {
	Symbol   string
	Qty      decimal.Decimal
	AvgPrice decimal.Decimal
	CurPrice decimal.Decimal
}

// ApplySync replaces portfolio state wholesale with authoritative data from the exchange.
// Called on create, on enable, and during periodic polling sync.
// Does NOT touch equityCurve or trades — those remain historical.
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

// RestorePosition injects a single position into the portfolio without touching cash.
// Used by the reconciler on startup to replay a position that was open when the app crashed.
// avgPrice is the original entry price; currentPrice is the latest market price.
func (p *Portfolio) RestorePosition(symbol, side string, qty, avgPrice, currentPrice decimal.Decimal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Negative qty represents a short position.
	if side == "sell" {
		qty = qty.Neg()
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

// UpdatePrice updates the current market price for a symbol.
// Recalculates unrealized PnL and market value.
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

// RecordEquity snapshots the current equity onto the equity curve.
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

// ApplyFill processes a fill, updating cash and positions.
// Records a completed Trade when a position is closed.
func (p *Portfolio) ApplyFill(fill Fill) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Cash impact
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

	// Update derived fields
	pos.CurrentPrice = fill.Price
	pos.UnrealizedPnL = pos.CurrentPrice.Sub(pos.AvgPrice).Mul(pos.Qty)
	pos.MarketValue = pos.Qty.Mul(pos.CurrentPrice)
}

// --- Metrics ---

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

// MaxDrawdown computes the maximum drawdown from the equity curve.
func (p *Portfolio) MaxDrawdown() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.MaxDrawdownLocked()
}

// WinRate returns the fraction of winning trades.
func (p *Portfolio) WinRate() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.winRateLocked()
}

// DailyPnL returns the PnL for today (sum of trades closed today).
func (p *Portfolio) DailyPnL() decimal.Decimal {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.dailyPnLLocked()
}

// Summary returns a snapshot of key portfolio metrics.
type Summary struct {
	InitialCapital decimal.Decimal `json:"initial_capital"`
	Cash           decimal.Decimal `json:"cash"`
	Equity         decimal.Decimal `json:"equity"`
	TotalReturn    float64         `json:"total_return_pct"`
	CurrentDD      float64         `json:"current_drawdown_pct"`
	MaxDD          float64         `json:"max_drawdown_pct"`
	WinRate        float64         `json:"win_rate_pct"`
	TotalTrades    int             `json:"total_trades"`
	OpenPositions  int             `json:"open_positions"`
	DailyPnL       decimal.Decimal `json:"daily_pnl"`
	Positions      []Position      `json:"positions"`
}

// Summary returns a full portfolio summary.
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
		MaxDD:          p.MaxDrawdownLocked() * 100,
		WinRate:        p.winRateLocked() * 100,
		TotalTrades:    len(p.trades),
		OpenPositions:  len(p.positions),
		DailyPnL:       p.dailyPnLLocked(),
		Positions:      positions,
	}
}

func (p *Portfolio) MaxDrawdownLocked() float64 {
	if len(p.equityCurve) == 0 {
		return 0
	}
	peak := p.equityCurve[0].Equity
	maxDD := decimal.Zero
	for _, pt := range p.equityCurve {
		if pt.Equity.GreaterThan(peak) {
			peak = pt.Equity
		}
		if peak.IsPositive() {
			dd := peak.Sub(pt.Equity).Div(peak)
			if dd.GreaterThan(maxDD) {
				maxDD = dd
			}
		}
	}
	f, _ := maxDD.Float64()
	return f
}

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
