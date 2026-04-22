package runtime

import (
	"orchestrator/internal/infra/exchange"
	"orchestrator/internal/module/bot/domain"
)

// BotInstance is the runtime view of a bot: stored data + Bot runner + Exchange.
// Used by the service layer to bridge persistence and execution.
type BotInstance struct {
	Data     *domain.BotInstance
	Bot      *Bot
	Exchange exchange.Exchange
}

func (b *BotInstance) Summary() domain.BotSummary {
	h := b.Bot.Health()
	m := b.Bot.Metrics()

	hv := domain.BotHealthView{
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

	mv := domain.BotMetricsView{
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

	return domain.BotSummary{
		ID:             b.Data.ID,
		Name:           b.Data.Name,
		Type:           b.Data.Type,
		Market:         b.Data.Market,
		OrchestratorID: b.Data.OrchestratorID,
		Strategy:       b.Data.Strategy,
		Position:       b.Data.Position,
		Risk:           b.Data.Risk,
		Symbols:        []string(b.Data.Symbols),
		Status:         b.Data.Status,
		Running:        b.Bot.IsRunning(),
		OrderCount:     len(b.Bot.Orders()),
		Health:         hv,
		Metrics:        mv,
		CreatedAt:      b.Data.CreatedAt,
	}
}
