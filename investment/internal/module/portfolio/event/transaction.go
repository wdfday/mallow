package event

import (
	"time"

	"github.com/google/uuid"
)

// TransactionRecorded is emitted when a user records any investment transaction.
// UserID is denormalized into the payload so projectors can write read-models keyed by user.
type TransactionRecorded struct {
	UserID          uuid.UUID       `json:"user_id"`
	Type            TransactionType `json:"type"`
	Symbol          string          `json:"symbol,omitempty"` // empty for deposit/withdrawal/fee
	Name            string          `json:"name,omitempty"`
	AssetType       string          `json:"asset_type,omitempty"`
	Exchange        string          `json:"exchange,omitempty"`
	Currency        string          `json:"currency"`
	Quantity        float64         `json:"quantity,omitempty"`
	PricePerUnit    float64         `json:"price_per_unit,omitempty"`
	Amount          float64         `json:"amount"` // total transaction amount
	Fees            float64         `json:"fees,omitempty"`
	Commission      float64         `json:"commission,omitempty"`
	Tax             float64         `json:"tax,omitempty"`
	RealizedGain    *float64        `json:"realized_gain,omitempty"` // only for sell
	RealizedGainPct *float64        `json:"realized_gain_pct,omitempty"`
	TransactionDate time.Time       `json:"transaction_date"`
	Broker          string          `json:"broker,omitempty"`
	Source          string          `json:"source,omitempty"` // "manual" | "sync"
	BotID           string          `json:"bot_id,omitempty"` // set by orchestrator bots, empty for manual/sync
	ExternalID      string          `json:"external_id,omitempty"`
	Notes           string          `json:"notes,omitempty"`
	// Derivative-specific fields (derivative_open / derivative_close)
	Underlying     string  `json:"underlying,omitempty"`
	InstrumentType string  `json:"instrument_type,omitempty"` // futures|perp|options|swap
	Side           string  `json:"side,omitempty"`            // long|short
	Leverage       float64 `json:"leverage,omitempty"`
	StrikePrice    float64 `json:"strike_price,omitempty"`
	OptionType     string  `json:"option_type,omitempty"` // call|put
	ExpiryDate     string  `json:"expiry_date,omitempty"` // date string
	ContractSize   float64 `json:"contract_size,omitempty"`
}

// AccountInitialized is emitted on the very first operation for an account.
type AccountInitialized struct {
	UserID        uuid.UUID `json:"user_id"`
	InitializedAt time.Time `json:"initialized_at"`
}
