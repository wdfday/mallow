package portfolio

import (
	"math"
	"sync"
	"time"
)

// Side represents the direction of a trade.
type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

// Position represents an open position for one symbol.
type Position struct {
	Symbol         string    `json:"symbol"`
	Qty            float64   `json:"qty"` // positive = long, negative = short
	AvgPrice       float64   `json:"avg_price"`
	CurrentPrice   float64   `json:"current_price"`
	UnrealizedPnL  float64   `json:"unrealized_pnl"`
	MarketValue    float64   `json:"market_value"`
	EntryTimestamp time.Time `json:"entry_timestamp"`
}

// Fill represents a confirmed order execution.
type Fill struct {
	Timestamp  time.Time `json:"timestamp"`
	Symbol     string    `json:"symbol"`
	Side       Side      `json:"side"`
	Qty        float64   `json:"qty"`
	Price      float64   `json:"price"`
	Commission float64   `json:"commission"`
}

// Trade represents a completed round-trip trade (entry + exit).
type Trade struct {
	Symbol         string    `json:"symbol"`
	Side           Side      `json:"side"`
	Qty            float64   `json:"qty"`
	EntryPrice     float64   `json:"entry_price"`
	ExitPrice      float64   `json:"exit_price"`
	EntryTimestamp time.Time `json:"entry_timestamp"`
	ExitTimestamp  time.Time `json:"exit_timestamp"`
	PnL            float64   `json:"pnl"`
	PnLPct         float64   `json:"pnl_pct"`
}

// EquityPoint is a snapshot of total equity at a point in time.
type EquityPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Equity    float64   `json:"equity"`
}

// Portfolio tracks cash, positions, equity curve, and completed trades.
// Thread-safe for concurrent access from signal handler and API.
type Portfolio struct {
	mu             sync.RWMutex
	initialCapital float64
	cash           float64
	positions      map[string]*Position
	equityCurve    []EquityPoint
	trades         []Trade
	peakEquity     float64
}

// New creates a Portfolio with the given initial capital.
func New(initialCapital float64) *Portfolio {
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
func (p *Portfolio) Cash() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cash
}

// Equity returns total equity = cash + sum of market values.
func (p *Portfolio) Equity() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.equityLocked()
}

func (p *Portfolio) equityLocked() float64 {
	mv := 0.0
	for _, pos := range p.positions {
		mv += pos.Qty * pos.CurrentPrice
	}
	return p.cash + mv
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
	Qty      float64
	AvgPrice float64
	CurPrice float64
}

// ApplySync replaces portfolio state wholesale with authoritative data from the exchange.
// Called on create, on enable, and during periodic polling sync.
// Does NOT touch equityCurve or trades — those remain historical.
func (p *Portfolio) ApplySync(cash float64, positions []SyncedPosition) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cash = cash
	p.positions = make(map[string]*Position, len(positions))
	for _, sp := range positions {
		mv := sp.Qty * sp.CurPrice
		p.positions[sp.Symbol] = &Position{
			Symbol:        sp.Symbol,
			Qty:           sp.Qty,
			AvgPrice:      sp.AvgPrice,
			CurrentPrice:  sp.CurPrice,
			UnrealizedPnL: (sp.CurPrice - sp.AvgPrice) * sp.Qty,
			MarketValue:   mv,
		}
	}
	eq := p.equityLocked()
	if eq > p.peakEquity {
		p.peakEquity = eq
	}
}

// UpdatePrice updates the current market price for a symbol.
// Recalculates unrealized PnL and market value.
func (p *Portfolio) UpdatePrice(symbol string, price float64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	pos, ok := p.positions[symbol]
	if !ok {
		return
	}
	pos.CurrentPrice = price
	pos.UnrealizedPnL = (price - pos.AvgPrice) * pos.Qty
	pos.MarketValue = pos.Qty * price
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
	if eq > p.peakEquity {
		p.peakEquity = eq
	}
}

// ApplyFill processes a fill, updating cash and positions.
// Records a completed Trade when a position is closed.
func (p *Portfolio) ApplyFill(fill Fill) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Cash impact
	gross := fill.Qty * fill.Price
	switch fill.Side {
	case SideBuy:
		p.cash -= (gross + fill.Commission)
	case SideSell:
		p.cash += (gross - fill.Commission)
	}

	pos, exists := p.positions[fill.Symbol]
	if !exists {
		pos = &Position{
			Symbol:         fill.Symbol,
			EntryTimestamp: fill.Timestamp,
		}
		p.positions[fill.Symbol] = pos
	}

	switch fill.Side {
	case SideBuy:
		newQty := pos.Qty + fill.Qty
		if newQty > 1e-9 {
			pos.AvgPrice = (pos.AvgPrice*pos.Qty + fill.Price*fill.Qty) / newQty
		}
		if pos.Qty <= 0 {
			pos.EntryTimestamp = fill.Timestamp
		}
		pos.Qty = newQty

	case SideSell:
		closedQty := math.Min(fill.Qty, math.Abs(pos.Qty))
		if closedQty > 0 && pos.Qty > 0 {
			pnl := (fill.Price-pos.AvgPrice)*closedQty - fill.Commission
			pnlPct := pnl / (pos.AvgPrice * closedQty)
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
		pos.Qty -= fill.Qty
		if math.Abs(pos.Qty) < 1e-9 {
			delete(p.positions, fill.Symbol)
			return
		}
	}

	// Update derived fields
	pos.CurrentPrice = fill.Price
	pos.UnrealizedPnL = (pos.CurrentPrice - pos.AvgPrice) * pos.Qty
	pos.MarketValue = pos.Qty * pos.CurrentPrice
}

// --- Metrics ---

// TotalReturn returns the total return as a fraction (e.g. 0.05 = 5%).
func (p *Portfolio) TotalReturn() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	eq := p.equityLocked()
	return (eq - p.initialCapital) / p.initialCapital
}

// CurrentDrawdown returns the current drawdown from peak equity as a fraction.
func (p *Portfolio) CurrentDrawdown() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	eq := p.equityLocked()
	if p.peakEquity <= 0 {
		return 0
	}
	return (p.peakEquity - eq) / p.peakEquity
}

// MaxDrawdown computes the maximum drawdown from the equity curve.
func (p *Portfolio) MaxDrawdown() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.equityCurve) == 0 {
		return 0
	}

	peak := p.equityCurve[0].Equity
	maxDD := 0.0
	for _, pt := range p.equityCurve {
		if pt.Equity > peak {
			peak = pt.Equity
		}
		dd := (peak - pt.Equity) / peak
		if dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD
}

// WinRate returns the fraction of winning trades.
func (p *Portfolio) WinRate() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.trades) == 0 {
		return 0
	}
	wins := 0
	for _, t := range p.trades {
		if t.PnL > 0 {
			wins++
		}
	}
	return float64(wins) / float64(len(p.trades))
}

// DailyPnL returns the PnL for today (sum of trades closed today).
func (p *Portfolio) DailyPnL() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	today := time.Now().UTC().Truncate(24 * time.Hour)
	pnl := 0.0
	for _, t := range p.trades {
		if t.ExitTimestamp.After(today) {
			pnl += t.PnL
		}
	}
	return pnl
}

// Summary returns a snapshot of key portfolio metrics.
type Summary struct {
	InitialCapital float64    `json:"initial_capital"`
	Cash           float64    `json:"cash"`
	Equity         float64    `json:"equity"`
	TotalReturn    float64    `json:"total_return_pct"`
	CurrentDD      float64    `json:"current_drawdown_pct"`
	MaxDD          float64    `json:"max_drawdown_pct"`
	WinRate        float64    `json:"win_rate_pct"`
	TotalTrades    int        `json:"total_trades"`
	OpenPositions  int        `json:"open_positions"`
	DailyPnL       float64    `json:"daily_pnl"`
	Positions      []Position `json:"positions"`
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
	if p.peakEquity > 0 {
		dd = (p.peakEquity - eq) / p.peakEquity * 100
	}

	return Summary{
		InitialCapital: p.initialCapital,
		Cash:           p.cash,
		Equity:         eq,
		TotalReturn:    (eq - p.initialCapital) / p.initialCapital * 100,
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
	maxDD := 0.0
	for _, pt := range p.equityCurve {
		if pt.Equity > peak {
			peak = pt.Equity
		}
		dd := (peak - pt.Equity) / peak
		if dd > maxDD {
			maxDD = dd
		}
	}
	return maxDD
}

func (p *Portfolio) winRateLocked() float64 {
	if len(p.trades) == 0 {
		return 0
	}
	wins := 0
	for _, t := range p.trades {
		if t.PnL > 0 {
			wins++
		}
	}
	return float64(wins) / float64(len(p.trades))
}

func (p *Portfolio) dailyPnLLocked() float64 {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	pnl := 0.0
	for _, t := range p.trades {
		if t.ExitTimestamp.After(today) {
			pnl += t.PnL
		}
	}
	return pnl
}
