package runtime

import (
	handdomain "mallow/helm/internal/module/hand/domain"

	"github.com/shopspring/decimal"
)

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
