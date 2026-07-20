package dto

import (
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/natsapi"
)

// ── Signals ──────────────────────────────────────────────────────────────────
// Audit trail of herald-originated signals only — see natsapi.SignalMsg's doc
// for why internal TP/SL/orphan signals never reach this table.

type SignalResp struct {
	ID          string          `json:"id"`
	HandID      string          `json:"hand_id"`
	Symbol      string          `json:"symbol"`
	Direction   string          `json:"direction"`
	ExitKind    string          `json:"exit_kind,omitempty"`
	Strength    float64         `json:"strength"`
	Price       decimal.Decimal `json:"price,omitzero"`
	TargetPrice decimal.Decimal `json:"target_price,omitzero"`
	StopPrice   decimal.Decimal `json:"stop_price,omitzero"`
	IsOffset    bool            `json:"is_offset,omitempty"`
	ATR         decimal.Decimal `json:"atr,omitzero"`
	Reason      string          `json:"reason,omitempty"`
	GeneratedAt time.Time       `json:"generated_at"`
	ReceivedAt  time.Time       `json:"received_at"`
}

type SignalPageResp struct {
	Signals []SignalResp `json:"signals"`
	Next    string       `json:"next,omitempty"` // RFC3339 cursor; empty = end
	HasMore bool         `json:"has_more"`
	Limit   int          `json:"limit"`
}

func SignalToResp(s natsapi.SignalMsg) SignalResp {
	return SignalResp{
		ID:          s.ID,
		HandID:      s.HandID,
		Symbol:      s.Symbol,
		Direction:   s.Direction,
		ExitKind:    s.ExitKind,
		Strength:    s.Strength,
		Price:       decimalOrZero(s.Price),
		TargetPrice: decimalOrZero(s.TargetPrice),
		StopPrice:   decimalOrZero(s.StopPrice),
		IsOffset:    s.IsOffset,
		ATR:         decimalOrZero(s.ATR),
		Reason:      s.Reason,
		GeneratedAt: s.GeneratedAt,
		ReceivedAt:  s.ReceivedAt,
	}
}

func decimalOrZero(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, _ := decimal.NewFromString(s)
	return d
}
