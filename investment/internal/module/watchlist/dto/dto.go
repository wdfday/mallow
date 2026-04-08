package dto

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/investment/internal/module/watchlist/domain"
)

type AddRequest struct {
	Symbol      string   `json:"symbol" binding:"required"`
	Name        string   `json:"name,omitempty"`
	AssetType   string   `json:"asset_type,omitempty"`
	TargetPrice *float64 `json:"target_price,omitempty"`
	Notes       string   `json:"notes,omitempty"`
}

type ItemResponse struct {
	ID          uuid.UUID        `json:"id"`
	Symbol      string           `json:"symbol"`
	Name        string           `json:"name,omitempty"`
	AssetType   string           `json:"asset_type,omitempty"`
	TargetPrice *decimal.Decimal `json:"target_price,omitempty"`
	Notes       string           `json:"notes,omitempty"`
	AddedAt     time.Time        `json:"added_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

func ToItemResponse(item domain.WatchlistItem) ItemResponse {
	return ItemResponse{
		ID:          item.ID,
		Symbol:      item.Symbol,
		Name:        item.Name,
		AssetType:   item.AssetType,
		TargetPrice: item.TargetPrice,
		Notes:       item.Notes,
		AddedAt:     item.AddedAt,
		UpdatedAt:   item.UpdatedAt,
	}
}
