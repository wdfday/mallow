package risk

import (
	"log/slog"
	"sync"
	"time"

	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/strategy"
)

// Manager enforces portfolio-level risk gates.
// It is a pure approve/reject gate — position sizing is the Tactician's job.
//
// Gate order on every non-exit intent:
//  1. Global halt (max drawdown breached in a prior call)
//  2. Daily loss halt (daily PnL limit breached today)
//  3. Max concurrent open units (entries only)
//
// Exit intents always bypass all gates — positions must be closeable regardless of risk state.
type Manager struct {
	mu        sync.RWMutex
	cfg       Config
	portfolio *portfolio.Portfolio
	halted    bool      // true if max drawdown was breached; reset only via ResetHalt
	haltedDay time.Time // UTC date when the daily loss limit last triggered

	// unitCounter counts total open position units (hand legs + manual portfolio positions).
	// When nil, falls back to len(portfolio.Positions()) as a best-effort estimate.
	unitCounter func() int
}

// SetUnitCounter injects a function that returns the total number of currently open
// position units across all hands and manual portfolio positions.
// Must be called before trading starts; safe to call concurrently.
func (m *Manager) SetUnitCounter(fn func() int) {
	m.mu.Lock()
	m.unitCounter = fn
	m.mu.Unlock()
}

// New creates a Manager with the given config and portfolio.
func New(cfg Config, p *portfolio.Portfolio) *Manager {
	return &Manager{cfg: cfg, portfolio: p}
}

// Validate checks portfolio-level risk gates before a trade intent is executed.
// Returns (approved=true, reason="") on success.
// Returns (approved=false, reason) when a gate blocks the intent.
func (m *Manager) Validate(intent strategy.Intent) (bool, string) {
	// Exit/close signals always pass — never block a position close.
	if intent.Signal.IsUrgent() || intent.Action.IsExit() {
		return true, ""
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Gate 1: global halt.
	if m.halted {
		return false, "trading halted: max drawdown breached"
	}

	// Gate 2: daily loss limit.
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
				slog.Warn("risk: daily loss limit breached",
					"daily_pnl", dailyPnL,
					"limit_pct", m.cfg.DailyLossLimitPct*100,
				)
				return false, "daily loss limit breached"
			}
		}

		// Gate 1b: max drawdown — halts permanently until ResetHalt.
		if dd := m.portfolio.CurrentDrawdown(); dd >= m.cfg.MaxDrawdownPct {
			m.halted = true
			slog.Warn("risk: max drawdown breached — halting all trading",
				"drawdown_pct", dd*100,
				"limit_pct", m.cfg.MaxDrawdownPct*100,
			)
			return false, "max drawdown breached"
		}
	}

	// Gate 3: max concurrent open units (entries only).
	// Counts all active legs across hands plus any manual portfolio positions not
	// owned by a hand. This correctly reflects pre-existing account exposure.
	if intent.Action.IsEntry() && m.cfg.MaxPositions >= 0 {
		// Adding to an existing position is always allowed under MaxPositions.
		if m.portfolio.GetPosition(intent.Signal.Symbol) == nil {
			var units int
			if m.unitCounter != nil {
				units = m.unitCounter()
			} else {
				units = len(m.portfolio.Positions())
			}
			if units >= m.cfg.MaxPositions {
				return false, "max positions reached"
			}
		}
	}

	return true, ""
}

// IsHalted reports whether trading has been globally halted by a max-drawdown breach.
func (m *Manager) IsHalted() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.halted
}

// ResetHalt clears both the global halt and the daily loss halt.
// The daily-loss condition will re-trigger on the next Validate call
// if the portfolio loss is still present — this is intentional.
func (m *Manager) ResetHalt() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.halted = false
	m.haltedDay = time.Time{}
	slog.Info("risk: halt reset")
}

// UpdateConfig replaces risk parameters at runtime. Preserves existing halt state.
func (m *Manager) UpdateConfig(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
	slog.Info("risk: config updated",
		"max_positions", cfg.MaxPositions,
		"daily_loss_limit_pct", cfg.DailyLossLimitPct,
		"max_drawdown_pct", cfg.MaxDrawdownPct,
	)
}
