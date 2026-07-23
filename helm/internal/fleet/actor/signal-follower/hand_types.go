package signalfollower

import (
	handdomain "mallow/helm/internal/module/hand/domain"
	"sync"
	"time"

	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/infra/natsapi"

	"github.com/shopspring/decimal"
)

// Signal is the canonical decoded signal type used throughout the runtime.
// All fields come from the herald protobuf SignalMsg + NATS metadata.
type Signal = strategy.Signal

// Health status values for HandHealth.Status.
// The first three mirror domain.HandStatus for the persisted lifecycle states.
// The remainder are runtime-only states not written to the DB.
const (
	HealthRunning  = "running"
	HealthStopped  = "stopped"
	HealthError    = "error"    // last order failed; clears on next successful order
	HealthKilled   = "killed"   // Kill() called; positions flattened
	HealthReleased = "released" // Release() called; positions orphaned
)

// HandHealth tracks liveness and error state.
type HandHealth struct {
	Status       string     `json:"status"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	LastSignalAt *time.Time `json:"last_signal_at,omitempty"`
	LastOrderAt  *time.Time `json:"last_order_at,omitempty"`
	LastErrorAt  *time.Time `json:"last_error_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Uptime       string     `json:"uptime,omitempty"`
}

// ── HandMetrics ───────────────────────────────────────────────────────────────

// HandMetrics tracks trading behavior counters.
type HandMetrics struct {
	SignalsReceived   int64           `json:"signals_received"`
	SignalsFiltered   int64           `json:"signals_filtered"`
	SignalsDropped    int64           `json:"signals_dropped"` // non-urgent signals lost due to full channel
	TradesApproved    int64           `json:"trades_approved"`
	OrdersPlaced      int64           `json:"orders_placed"`
	OrdersFilled      int64           `json:"orders_filled"`
	OrdersFailed      int64           `json:"orders_failed"`
	TotalPnL          decimal.Decimal `json:"total_pnl"`
	TotalCommission   decimal.Decimal `json:"total_commission"`
	WinCount          int64           `json:"win_count"`
	LossCount         int64           `json:"loss_count"`
	LatestSignalLagMs int64           `json:"latest_signal_lag_ms"` // end-to-end lag of the last signal processed
	SignalQueueDepth  int             `json:"signal_queue_depth"`   // pending signals in the hand channel
}

// Metrics returns a snapshot of the hand's trading counters and P&L.
func (h *Hand) Metrics() HandMetrics {
	h.metrics.mu.Lock()
	pnl := h.metrics.totalPnL
	commission := h.metrics.totalCommission
	wins := h.metrics.winCount
	losses := h.metrics.lossCount
	h.metrics.mu.Unlock()
	return HandMetrics{
		SignalsReceived:   h.metrics.signalsReceived.Load(),
		SignalsFiltered:   h.metrics.signalsFiltered.Load(),
		SignalsDropped:    h.metrics.signalsDropped.Load(),
		TradesApproved:    h.metrics.tradesApproved.Load(),
		OrdersPlaced:      h.metrics.ordersPlaced.Load(),
		OrdersFilled:      h.metrics.ordersFilled.Load(),
		OrdersFailed:      h.metrics.ordersFailed.Load(),
		TotalPnL:          pnl,
		TotalCommission:   commission,
		WinCount:          wins,
		LossCount:         losses,
		LatestSignalLagMs: h.metrics.latestSignalLagMs.Load(),
		SignalQueueDepth:  len(h.Signals),
	}
}

// MetricsView converts live HandMetrics into the domain DTO.
// Call this before removing the hand from memory to snapshot its final state.
func (h *Hand) MetricsView() handdomain.HandMetricsView {
	m := h.Metrics()
	return handdomain.HandMetricsView{
		SignalsReceived:   m.SignalsReceived,
		SignalsFiltered:   m.SignalsFiltered,
		SignalsDropped:    m.SignalsDropped,
		TradesApproved:    m.TradesApproved,
		OrdersPlaced:      m.OrdersPlaced,
		OrdersFilled:      m.OrdersFilled,
		OrdersFailed:      m.OrdersFailed,
		TotalPnL:          m.TotalPnL,
		TotalCommission:   m.TotalCommission,
		WinCount:          m.WinCount,
		LossCount:         m.LossCount,
		LatestSignalLagMs: m.LatestSignalLagMs,
		SignalQueueDepth:  m.SignalQueueDepth,
	}
}

// ── handEventBus — test-only broadcast ───────────────────────────────────────

// handEventBus is a simple fan-out for test observability.
// EmitEvent broadcasts every HelmEvent to all registered subscriber channels.
// Nil-safe: all methods are no-ops when bus is nil (production path).
type handEventBus struct {
	mu   sync.Mutex
	subs []chan natsapi.HelmEvent
}

// subscribe returns a new channel that will receive all future events.
func (b *handEventBus) subscribe(cap int) <-chan natsapi.HelmEvent {
	ch := make(chan natsapi.HelmEvent, cap)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

// publish sends ev to every registered subscriber, non-blocking.
func (b *handEventBus) publish(ev natsapi.HelmEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default: // subscriber is slow; drop rather than block the run-loop
		}
	}
}
