package dto

import "time"

// StatsHandSummary is the per-hand slice of a runtime stats response.
type StatsHandSummary struct {
	HandID           string  `json:"hand_id"`
	Symbol           string  `json:"symbol"`
	Strategy         string  `json:"strategy"`
	Status           string  `json:"status"`
	SignalsReceived  int64   `json:"signals_received"`
	SignalsFiltered  int64   `json:"signals_filtered"`
	SignalsDropped   int64   `json:"signals_dropped"`
	OrdersPlaced     int64   `json:"orders_placed"`
	OrdersFilled     int64   `json:"orders_filled"`
	OrdersFailed     int64   `json:"orders_failed"`
	TotalPnL         float64 `json:"total_pnl"`
	WinCount         int64   `json:"win_count"`
	LossCount        int64   `json:"loss_count"`
	SignalLagLastMs  int64   `json:"signal_lag_last_ms"`
	SignalQueueDepth int     `json:"signal_queue_depth"`
}

// StatsHelmSummary is the per-helm slice of a runtime stats response.
type StatsHelmSummary struct {
	HelmID       string             `json:"helm_id"`
	AccountID    string             `json:"account_id"`
	BrokerType   string             `json:"broker_type"`
	Paused       bool               `json:"paused"`
	Halted       bool               `json:"halted"`
	Equity       float64            `json:"equity"`
	Cash         float64            `json:"cash"`
	DailyPnL     float64            `json:"daily_pnl"`
	DrawdownPct  float64            `json:"drawdown_pct"`
	RunningHands int                `json:"running_hands"`
	LastSyncAt   *time.Time         `json:"last_sync_at,omitempty"`
	Hands        []StatsHandSummary `json:"hands"`
}

// StatsGlobal is the global/aggregate portion of a runtime stats response.
type StatsGlobal struct {
	RunningHands     int   `json:"running_hands"`
	ActiveRuntimes   int   `json:"active_runtimes"`
	RouteNoHelm      int64 `json:"dispatch_route_no_helm"`
	RouteNoHand      int64 `json:"dispatch_route_no_hand"`
	NATSSignalsTotal int64 `json:"nats_signals_total"`
	NATSDispatched   int64 `json:"nats_signals_dispatched"`
	NATSMissingID    int64 `json:"nats_signals_missing_id"`
	NATSNilPayload   int64 `json:"nats_signals_nil_payload"`
}

// StatsResponse is the full response for the runtime stats NATS subject.
type StatsResponse struct {
	Helms  []StatsHelmSummary `json:"helms"`
	Global StatsGlobal        `json:"global"`
}
