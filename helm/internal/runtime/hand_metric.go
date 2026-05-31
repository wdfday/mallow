package runtime

import (
	"mallow/helm/internal/infra/exchange"
	handdomain "mallow/helm/internal/module/hand/domain"
	"time"

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

// ── HandRef ───────────────────────────────────────────────────────────────────

// HandRef is the runtime view of a hand: stored data + Hand runner + Exchange.
// Used by the service layer to bridge persistence and execution.
type HandRef struct {
	Data     *handdomain.Hand
	Runner   *Hand
	Exchange exchange.Exchange
}

// Summary returns a lightweight HandSummary from live runtime state.
func (b *HandRef) Summary() handdomain.HandSummary {
	h := b.Runner.Health()
	m := b.Runner.Metrics()

	hv := handdomain.HandHealthView{
		Status:    h.Status,
		LastError: h.LastError,
		Uptime:    h.Uptime,
	}
	if h.StartedAt != nil && !h.StartedAt.IsZero() {
		hv.StartedAt = h.StartedAt.Format(time.RFC3339)
	}
	if h.LastSignalAt != nil && !h.LastSignalAt.IsZero() {
		hv.LastSignalAt = h.LastSignalAt.Format(time.RFC3339)
	}
	if h.LastOrderAt != nil && !h.LastOrderAt.IsZero() {
		hv.LastOrderAt = h.LastOrderAt.Format(time.RFC3339)
	}
	if h.LastErrorAt != nil && !h.LastErrorAt.IsZero() {
		hv.LastErrorAt = h.LastErrorAt.Format(time.RFC3339)
	}

	mv := handdomain.HandMetricsView{
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

	return handdomain.HandSummary{
		ID:               b.Data.ID,
		Name:             b.Data.Name,
		Type:             b.Data.Type,
		Market:           b.Data.Market,
		HelmID:           b.Data.HelmID,
		Strategy:         b.Data.Strategy,
		Position:         b.Data.Position,
		Guard:            b.Data.Guard,
		Symbols:          []string(b.Data.Symbols),
		Status:           b.Data.Status,
		Running:          b.Runner.IsRunning(),
		OrderCount:       len(b.Runner.Orders()),
		Health:           hv,
		Metrics:          mv,
		Futures:          b.Data.Futures,
		CreatedAt:        b.Data.CreatedAt,
		AllocatedCapital: b.Data.AllocatedCapital,
		DeployedCapital:  b.Runner.DeployedCapital(),
		AvailableCash:    b.Runner.AvailableCash(),
		SignalTTLSec:     b.Data.SignalTTLSec,
	}
}
