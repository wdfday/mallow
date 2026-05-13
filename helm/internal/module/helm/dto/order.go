package dto

import (
	"time"

	"github.com/shopspring/decimal"

	botdomain "mallow/helm/internal/module/hand/domain"
)

type OrderResp struct {
	ID             string          `json:"id"`
	BotID          string          `json:"hand_id"`
	OrchestratorID string          `json:"helm_id"`
	Symbol         string          `json:"symbol"`
	Side           string          `json:"side"`
	Qty            decimal.Decimal `json:"qty"`
	Type           string          `json:"type"`
	Status         string          `json:"status"`
	FilledQty      decimal.Decimal `json:"filled_qty"`
	FilledAvg      decimal.Decimal `json:"filled_avg_price"`
	SubmitTime     time.Time       `json:"submitted_at"`
}

func OrderToResp(o botdomain.Order) OrderResp {
	return OrderResp{
		ID:             o.ID,
		BotID:          o.HandId,
		OrchestratorID: o.HelmId,
		Symbol:         o.Symbol,
		Side:           o.Side,
		Qty:            o.Qty,
		Type:           o.Type,
		Status:         o.Status,
		FilledQty:      o.FilledQty,
		FilledAvg:      o.FilledAvg,
		SubmitTime:     o.SubmitTime,
	}
}
