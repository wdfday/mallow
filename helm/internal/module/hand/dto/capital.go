package dto

import "github.com/shopspring/decimal"

// CapitalSuggestion is one hand that can free up capital for a new hand.
type CapitalSuggestion struct {
	HandID          string          `json:"hand_id"`
	Name            string          `json:"name"`
	Allocated       decimal.Decimal `json:"allocated"`
	Deployed        decimal.Decimal `json:"deployed"`
	ReducibleBy     decimal.Decimal `json:"reducible_by"`
	SuggestedTarget decimal.Decimal `json:"suggested_target"`
}

// CapitalOverflow is returned (HTTP 422) when a new or updated hand would exceed
// the helm's unallocated capital. It carries suggestions so the caller knows exactly
// which hands can be reduced and by how much.
//
// Field meanings (all in the helm's quote currency):
//
//	helm_equity      = Portfolio.Summary().Cash                   — liquid broker cash
//	total_allocated  = Σ existing_hand.AllocatedCapital           — excluding the hand being updated
//	requested        = the AllocatedCapital being created/updated
//	available        = helm_cash − total_allocated
type CapitalOverflow struct {
	Error       string              `json:"error"`
	HelmEquity  float64             `json:"helm_equity"`
	TotalAlloc  float64             `json:"total_allocated"`
	Requested   float64             `json:"requested"`
	Available   float64             `json:"available"`
	Suggestions []CapitalSuggestion `json:"suggestions"`
}
