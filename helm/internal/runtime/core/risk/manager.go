package risk

import (
	"log/slog"
	"sync"
	"time"

	"github.com/shopspring/decimal"

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
	mu             sync.RWMutex
	cfg            Config
	portfolio      *portfolio.Portfolio
	halted         bool      // true if max drawdown was breached; reset only via ResetHalt
	haltedDay      time.Time // UTC date when the daily loss limit last triggered
	handOrderTimes map[string][]time.Time

	// unitCounter counts total open position units (hand legs + manual portfolio positions).
	// When nil, falls back to len(portfolio.Positions()) as a best-effort estimate.
	unitCounter func() int

	// availableCashFn returns the helm's available cash: totalCash − Σ(hand cash budgets).
	// When non-nil, Gate 0.5 blocks new entries whenever the value is ≤ 0 — meaning the
	// account's real cash has dropped below what the hands collectively expect to have.
	availableCashFn func() decimal.Decimal
}

// SetUnitCounter injects a function that returns the total number of currently open
// position units across all hands and manual portfolio positions.
// Must be called before trading starts; safe to call concurrently.
func (m *Manager) SetUnitCounter(fn func() int) {
	m.mu.Lock()
	m.unitCounter = fn
	m.mu.Unlock()
}

// SetAvailableCashFn injects a function that returns the helm's available cash:
//
//	totalCash − Σ(allocatedCap + closedPnL − deployed)  for each hand
//
// Gate 0.5 blocks new entries when the returned value is ≤ 0, meaning the account's
// real cash balance has dropped below what the hands collectively expect to hold —
// a capital-adequacy circuit-breaker. When nil (default), the gate is disabled.
// Must be called before trading starts; safe to call concurrently.
func (m *Manager) SetAvailableCashFn(fn func() decimal.Decimal) {
	m.mu.Lock()
	m.availableCashFn = fn
	m.mu.Unlock()
}

// New creates a Manager with the given config and portfolio.
func New(cfg Config, p *portfolio.Portfolio) *Manager {
	return &Manager{
		cfg:            cfg,
		portfolio:      p,
		handOrderTimes: make(map[string][]time.Time),
	}
}

// Validate checks portfolio-level risk gates before a trade intent is executed.
// Returns (approved=true, reason="") on success.
// Returns (approved=false, reason) when a gate blocks the intent.
func (m *Manager) Validate(intent strategy.Intent, handID string) (bool, string) {
	// Exit/close signals always pass — never block a position close.
	if intent.Signal.IsUrgent() || intent.Action.IsExit() {
		return true, ""
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Gate 0.5: capital adequacy — available cash must cover hand budgets.
	// Fires when totalCash < Σ(hand cash budgets), i.e. real account cash can no longer
	// back the capital the hands expect to operate with.
	if m.availableCashFn != nil {
		if !m.availableCashFn().IsPositive() {
			slog.Warn("risk: available cash depleted — blocking new entries")
			return false, "insufficient available cash"
		}
	}

	// Gate 1: global halt.
	if m.halted {
		return false, "trading halted: max drawdown breached"
	}

	// Gate 2: daily loss limit.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if m.haltedDay.Equal(today) {
		return false, "trading halted: daily loss limit breached"
	}

	// Gate 2.5: Order frequency / loop protection (if enabled and handID is provided)
	if handID != "" && m.cfg.MaxOrderRateLimit > 0 && m.cfg.OrderRateWindowSec > 0 {
		now := time.Now()
		window := time.Duration(m.cfg.OrderRateWindowSec) * time.Second
		cutoff := now.Add(-window)

		// Filter existing timestamps to keep only those within the window
		times := m.handOrderTimes[handID]
		var activeTimes []time.Time
		for _, t := range times {
			if t.After(cutoff) {
				activeTimes = append(activeTimes, t)
			}
		}

		if len(activeTimes) >= m.cfg.MaxOrderRateLimit {
			m.halted = true // permanently halt trading for safety
			slog.Warn("risk: order frequency limit exceeded — halting all trading",
				"hand_id", handID,
				"window_sec", m.cfg.OrderRateWindowSec,
				"limit", m.cfg.MaxOrderRateLimit,
				"actual_count", len(activeTimes),
			)
			return false, "order frequency limit exceeded"
		}

		// Append new timestamp and update map
		activeTimes = append(activeTimes, now)
		m.handOrderTimes[handID] = activeTimes
	}

	equity := m.portfolio.Equity()
	if equity.IsPositive() {
		// All percentage gates are opt-in: 0 = disabled (not "halt on any loss").
		dailyPnL := m.portfolio.DailyPnL()
		if m.cfg.DailyLossLimitPct > 0 && dailyPnL.IsNegative() {
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

		// Gate 1b: max drawdown — halts permanently until ResetHalt. 0 = disabled.
		if m.cfg.MaxDrawdownPct > 0 {
			if dd := m.portfolio.CurrentDrawdown(); dd >= m.cfg.MaxDrawdownPct {
				m.halted = true
				slog.Warn("risk: max drawdown breached — halting all trading",
					"drawdown_pct", dd*100,
					"limit_pct", m.cfg.MaxDrawdownPct*100,
				)
				return false, "max drawdown breached"
			}
		}
	}

	// Gate 3: max concurrent open units (entries only). 0 = unlimited.
	// Counts all active legs across hands plus any manual portfolio positions not
	// owned by a hand. This correctly reflects pre-existing account exposure.
	if intent.Action.IsEntry() && m.cfg.MaxPositions > 0 {
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

	// Gate 4: account gross-exposure ceiling (entries AND pyramid adds).
	// Unlike Gate 3 this binds adds — it is the account-blowup backstop that lets hands
	// pyramid aggressively up to a known notional limit. Coarse: blocks once already at the
	// ceiling (the incoming order may push slightly past, same as MaxPositions).
	if intent.Action.IsEntry() && m.cfg.MaxGrossExposurePct > 0 && equity.IsPositive() {
		ceiling := equity.Mul(decimal.NewFromFloat(m.cfg.MaxGrossExposurePct))
		if m.portfolio.GrossExposure().GreaterThanOrEqual(ceiling) {
			return false, "max gross exposure reached"
		}
	}

	return true, ""
}

// IsHalted reports whether trading has been globally halted by a max-drawdown or daily loss breach.
func (m *Manager) IsHalted() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.halted {
		return true
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	return m.haltedDay.Equal(today)
}

// ResetHalt clears both the global halt and the daily loss halt.
// The daily-loss condition will re-trigger on the next Validate call
// if the portfolio loss is still present — this is intentional.
func (m *Manager) ResetHalt() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.halted = false
	m.haltedDay = time.Time{}
	m.handOrderTimes = make(map[string][]time.Time)
	// Rebase the drawdown high-water mark to current equity so the max-drawdown gate
	// measures loss from the post-reset level. Without this the next Validate recomputes
	// the same drawdown from the stale peak and immediately re-halts.
	m.portfolio.ResetPeakToCurrentEquity()
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
		"max_gross_exposure_pct", cfg.MaxGrossExposurePct,
	)
}
