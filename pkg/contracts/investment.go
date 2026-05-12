// Package contracts defines shared NATS message schemas between services.
// Consumers and producers of a subject must agree on this struct.
package contracts

import "time"

// InvestmentTransactionMsg is the payload for the NATS subject
// "investment.transactions.{accountID}".
//
// Published by:  orchestrator bots, identity broker sync service.
// Consumed by:   investment service pull consumer (INVESTMENT_TRANSACTIONS stream).
//
// AccountID is the aggregate root (investment account).
// UserID is the owner — used by read-model projections.
//
// Transaction types:
//
//	buy | sell | dividend | deposit | withdrawal | fee
//	derivative_open | derivative_close
type InvestmentTransactionMsg struct {
	AccountID       string    `json:"account_id"`
	UserID          string    `json:"user_id"`
	Type            string    `json:"type"`
	Symbol          string    `json:"symbol,omitempty"`
	Name            string    `json:"name,omitempty"`
	AssetType       string    `json:"asset_type,omitempty"`
	Exchange        string    `json:"exchange,omitempty"`
	Currency        string    `json:"currency"`
	Quantity        float64   `json:"quantity,omitempty"`
	PricePerUnit    float64   `json:"price_per_unit,omitempty"`
	Amount          float64   `json:"amount"`
	Fees            float64   `json:"fees,omitempty"`
	Commission      float64   `json:"commission,omitempty"`
	Tax             float64   `json:"tax,omitempty"`
	TransactionDate time.Time `json:"transaction_date"`
	ExternalID      string    `json:"external_id,omitempty"` // orderID (for grouping fills of the same order)
	TradeID         string    `json:"trade_id,omitempty"`    // exchange fill ID — primary dedup key
	Kind            string    `json:"kind,omitempty"`        // "open_order" | "fill" | "cancel"
	Notes           string    `json:"notes,omitempty"`
	Source          string    `json:"source,omitempty"` // "manual" | "sync"
	BotID           string    `json:"bot_id,omitempty"` // set by orchestrator bots, empty for manual/sync

	// Derivative-specific (only for derivative_open / derivative_close)
	Underlying     string  `json:"underlying,omitempty"`
	InstrumentType string  `json:"instrument_type,omitempty"`
	Side           string  `json:"side,omitempty"`
	Leverage       float64 `json:"leverage,omitempty"`
	StrikePrice    float64 `json:"strike_price,omitempty"`
	OptionType     string  `json:"option_type,omitempty"`
	ExpiryDate     string  `json:"expiry_date,omitempty"`
	ContractSize   float64 `json:"contract_size,omitempty"`
}
