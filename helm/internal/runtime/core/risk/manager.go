package risk

import (
	"log/slog"
	"sync"
	"time"

	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/strategy"
)

// Config holds risk management parameters.
type Config struct {
	MaxPositions      int     `json:"max_positions"`
	MaxPositionPct    float64 `json:"max_position_pct"`     // max fraction of equity per position
	DailyLossLimitPct float64 `json:"daily_loss_limit_pct"` // halt day if daily loss exceeds this
	MaxDrawdownPct    float64 `json:"max_drawdown_pct"`     // halt all trading if drawdown exceeds this
}

func DefaultConfig() Config {
	return Config{
		MaxPositions:      5,
		MaxPositionPct:    0.10,
		DailyLossLimitPct: 0.02,
		MaxDrawdownPct:    0.10,
	}
}

// Manager enforces portfolio-level risk gates.
// Sizing is delegated to Tactician; this manager is a pure gate (approve/reject).
type Manager struct {
	mu        sync.RWMutex
	cfg       Config
	portfolio *portfolio.Portfolio
	halted    bool      // true if max drawdown breached
	haltedDay time.Time // date when daily loss limit was breached
}

func New(cfg Config, p *portfolio.Portfolio) *Manager {
	return &Manager{cfg: cfg, portfolio: p}
}

// Validate checks portfolio-level risk gates before a trade intent is executed.
// Exit intents always pass — we must be able to close positions regardless of risk state.
// Returns (approved, reason); reason is non-empty only when approved=false.
func (m *Manager) Validate(intent strategy.Intent) (bool, string) {
	// Exit/close signals bypass all risk gates.
	if intent.Signal.IsUrgent() || intent.Action.IsExit() {
		return true, ""
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Global halt (max drawdown breached).
	if m.halted {
		return false, "trading halted: max drawdown breached"
	}

	// Daily loss limit.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if m.haltedDay.Equal(today) {
		return false, "trading halted: daily loss limit breached"
	}

	equity := m.portfolio.Equity()
	if equity.IsPositive() {
		dailyPnL := m.portfolio.DailyPnL()
		if dailyPnL.IsNegative() {
			lossRatio, _ := dailyPnL.Neg().Div(equity).Float64()
			if lossRatio >= m.cfg.DailyLossLimitPct {
				m.haltedDay = today
				slog.Warn("daily loss limit breached",
					"daily_pnl", dailyPnL,
					"limit_pct", m.cfg.DailyLossLimitPct*100,
				)
				return false, "daily loss limit breached"
			}
		}

		// Max drawdown — halts all future trading until ResetHalt.
		if dd := m.portfolio.CurrentDrawdown(); dd >= m.cfg.MaxDrawdownPct {
			m.halted = true
			slog.Warn("max drawdown breached — halting all trading",
				"drawdown_pct", dd*100,
				"limit_pct", m.cfg.MaxDrawdownPct*100,
			)
			return false, "max drawdown breached"
		}
	}

	// Max simultaneous positions (entries only).
	if intent.Action.IsEntry() {
		positions := m.portfolio.Positions()
		if len(positions) >= m.cfg.MaxPositions {
			for _, p := range positions {
				if p.Symbol == intent.Signal.Symbol {
					return true, "" // adding to existing position — allow
				}
			}
			return false, "max positions reached"
		}
	}

	return true, ""
}

// IsHalted returns true if trading has been globally halted.
func (m *Manager) IsHalted() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.halted
}

// ResetHalt clears halt flags. Provides a manual one-shot override.
// Note: daily loss conditions will re-trigger on the next Validate call
// if the portfolio loss is still present — this is intentional.
func (m *Manager) ResetHalt() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.halted = false
	m.haltedDay = time.Time{}
	slog.Info("risk halt reset")
}

// UpdateConfig replaces risk parameters at runtime.
// Preserves existing halt state.
func (m *Manager) UpdateConfig(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
	slog.Info("risk config updated",
		"max_positions", cfg.MaxPositions,
		"max_position_pct", cfg.MaxPositionPct,
		"daily_loss_limit_pct", cfg.DailyLossLimitPct,
		"max_drawdown_pct", cfg.MaxDrawdownPct,
	)
}
