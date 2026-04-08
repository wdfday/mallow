package event

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
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
	Quantity        decimal.Decimal  `json:"quantity,omitempty"`
	PricePerUnit    decimal.Decimal  `json:"price_per_unit,omitempty"`
	Amount          decimal.Decimal  `json:"amount"` // total transaction amount
	Fees            decimal.Decimal  `json:"fees,omitempty"`
	Commission      decimal.Decimal  `json:"commission,omitempty"`
	Tax             decimal.Decimal  `json:"tax,omitempty"`
	RealizedGain    *decimal.Decimal `json:"realized_gain,omitempty"` // only for sell
	RealizedGainPct *float64         `json:"realized_gain_pct,omitempty"`
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
	Leverage       float64         `json:"leverage,omitempty"`
	StrikePrice    decimal.Decimal `json:"strike_price,omitempty"`
	OptionType     string          `json:"option_type,omitempty"` // call|put
	ExpiryDate     string          `json:"expiry_date,omitempty"` // date string
	ContractSize   decimal.Decimal `json:"contract_size,omitempty"`
}

// AccountInitialized is emitted on the very first operation for an account.
type AccountInitialized struct {
	UserID        uuid.UUID `json:"user_id"`
	InitializedAt time.Time `json:"initialized_at"`
}
