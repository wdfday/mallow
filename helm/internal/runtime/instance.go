package runtime

import (
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/module/hand/domain"
)

// HandRef is the runtime view of a hand: stored data + Hand runner + Exchange.
// Used by the service layer to bridge persistence and execution.
type HandRef struct {
	Data     *domain.Hand
	Runner   *Hand
	Exchange exchange.Exchange
}

func (b *HandRef) Summary() domain.HandSummary {
	h := b.Runner.Health()
	m := b.Runner.Metrics()

	hv := domain.HandHealthView{
		Status:    h.Status,
		LastError: h.LastError,
		Uptime:    h.Uptime,
	}
	if !h.StartedAt.IsZero() {
		hv.StartedAt = h.StartedAt.Format("2006-01-02T15:04:05Z")
	}
	if !h.LastSignalAt.IsZero() {
		hv.LastSignalAt = h.LastSignalAt.Format("2006-01-02T15:04:05Z")
	}
	if !h.LastOrderAt.IsZero() {
		hv.LastOrderAt = h.LastOrderAt.Format("2006-01-02T15:04:05Z")
	}
	if !h.LastErrorAt.IsZero() {
		hv.LastErrorAt = h.LastErrorAt.Format("2006-01-02T15:04:05Z")
	}

	mv := domain.HandMetricsView{
		SignalsReceived: m.SignalsReceived,
		SignalsFiltered: m.SignalsFiltered,
		TradesApproved:  m.TradesApproved,
		OrdersPlaced:    m.OrdersPlaced,
		OrdersFilled:    m.OrdersFilled,
		OrdersFailed:    m.OrdersFailed,
		TotalPnL:        m.TotalPnL,
		WinCount:        m.WinCount,
		LossCount:       m.LossCount,
	}

	return domain.HandSummary{
		ID:         b.Data.ID,
		Name:       b.Data.Name,
		Type:       b.Data.Type,
		Market:     b.Data.Market,
		HelmID:     b.Data.HelmID,
		Strategy:   b.Data.Strategy,
		Position:   b.Data.Position,
		Risk:       b.Data.Risk,
		Symbols:    []string(b.Data.Symbols),
		Status:     b.Data.Status,
		Running:    b.Runner.IsRunning(),
		OrderCount: len(b.Runner.Orders()),
		Health:     hv,
		Metrics:    mv,
		CreatedAt:  b.Data.CreatedAt,
	}
}
