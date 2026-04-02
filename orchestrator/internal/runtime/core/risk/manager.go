package risk

import (
	"log/slog"
	"orchestrator/internal/infra/engine"
	"orchestrator/internal/runtime/core/portfolio"
	"time"
)

// Config holds risk management parameters.
type Config struct {
	// MaxPositions is the maximum number of simultaneous open positions.
	MaxPositions int `json:"max_positions"`

	// MaxPositionPct is the maximum fraction of equity per position (e.g. 0.10 = 10%).
	MaxPositionPct float64 `json:"max_position_pct"`

	// DailyLossLimitPct is the maximum daily loss as a fraction of initial capital.
	// Trading is halted for the day if breached (e.g. 0.02 = 2%).
	DailyLossLimitPct float64 `json:"daily_loss_limit_pct"`

	// MaxDrawdownPct is the maximum drawdown from peak equity.
	// All trading is halted if breached (e.g. 0.10 = 10%).
	MaxDrawdownPct float64 `json:"max_drawdown_pct"`
}

// DefaultConfig returns sensible defaults for paper trading.
func DefaultConfig() Config {
	return Config{
		MaxPositions:      5,
		MaxPositionPct:    0.10,
		DailyLossLimitPct: 0.02,
		MaxDrawdownPct:    0.10,
	}
}

// Manager validates signals and sizes positions based on risk rules.
type Manager struct {
	cfg       Config
	portfolio *portfolio.Portfolio
	halted    bool      // true if max drawdown breached
	haltedDay time.Time // date when daily loss limit was breached
}

// New creates a RiskManager with the given config and portfolio reference.
func New(cfg Config, p *portfolio.Portfolio) *Manager {
	return &Manager{
		cfg:       cfg,
		portfolio: p,
	}
}

// Validate checks whether a signal should be acted upon.
// Returns (approved bool, reason string).
func (m *Manager) Validate(signal *engine.SignalMsg) (bool, string) {
	// Close signals are always allowed — we want to be able to exit positions.
	if signal.Dir == "close" {
		return true, ""
	}

	// Check global halt (max drawdown breached).
	if m.halted {
		return false, "trading halted: max drawdown breached"
	}

	// Check daily loss limit.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if m.haltedDay.Equal(today) {
		return false, "trading halted: daily loss limit breached"
	}

	dailyPnL := m.portfolio.DailyPnL()
	equity := m.portfolio.Equity()
	if equity > 0 && dailyPnL < 0 && (-dailyPnL/equity) >= m.cfg.DailyLossLimitPct {
		m.haltedDay = today
		slog.Warn("daily loss limit breached",
			"daily_pnl", dailyPnL,
			"limit_pct", m.cfg.DailyLossLimitPct*100,
		)
		return false, "daily loss limit breached"
	}

	// Check max drawdown.
	dd := m.portfolio.CurrentDrawdown()
	if dd >= m.cfg.MaxDrawdownPct {
		m.halted = true
		slog.Warn("max drawdown breached — halting all trading",
			"drawdown_pct", dd*100,
			"limit_pct", m.cfg.MaxDrawdownPct*100,
		)
		return false, "max drawdown breached"
	}

	// Check position count limit (only for new entries, not closes).
	positions := m.portfolio.Positions()
	if len(positions) >= m.cfg.MaxPositions {
		// Allow if we already have a position in this symbol (adding to existing).
		hasPosition := false
		for _, p := range positions {
			if p.Symbol == signal.S {
				hasPosition = true
				break
			}
		}
		if !hasPosition {
			return false, "max positions reached"
		}
	}

	return true, ""
}

// Size returns the quantity to trade for a given signal and current price.
// Uses fixed fractional sizing: allocate MaxPositionPct of equity per trade.
func (m *Manager) Size(signal *engine.SignalMsg, price float64) float64 {
	if price <= 0 {
		return 0
	}

	equity := m.portfolio.Equity()
	alloc := equity * m.cfg.MaxPositionPct

	// Scale by signal strength [0, 1].
	alloc *= signal.Strength

	qty := alloc / price

	// For stocks, round down to whole shares.
	// For crypto, allow fractional.
	// Simple heuristic: if price > 1, round to whole shares.
	if price > 1.0 {
		qty = float64(int(qty))
	}

	if qty < 0 {
		qty = 0
	}

	return qty
}

// IsHalted returns true if trading has been halted due to risk limits.
func (m *Manager) IsHalted() bool {
	return m.halted
}

// ResetHalt clears the global halt flag (e.g. manual override).
func (m *Manager) ResetHalt() {
	m.halted = false
	m.haltedDay = time.Time{}
	slog.Info("risk halt reset")
}
