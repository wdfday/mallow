package readmodel

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// EventRecord is a persisted helm/hand behavioral event (the `helm_events` read model).
// Mirrors natsapi.HelmEvent but with a typed user_id for DB queries.
type EventRecord struct {
	ID        int64           `json:"id"`
	At        time.Time       `json:"at"`
	HelmID    uuid.UUID       `json:"helm_id"`
	HandID    *uuid.UUID      `json:"hand_id,omitempty"`
	UserID    uuid.UUID       `json:"user_id"`
	Code      int             `json:"code"`
	Symbol    string          `json:"symbol,omitempty"`
	Direction string          `json:"direction,omitempty"`
	Side      string          `json:"side,omitempty"`
	Qty       decimal.Decimal `json:"qty,omitempty"`
	Price     decimal.Decimal `json:"price,omitempty"`
	OrderID   string          `json:"order_id,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Msg       string          `json:"msg,omitempty"`
}

// EventFilter constrains an events Query call.
type EventFilter struct {
	HelmID uuid.UUID  // required
	HandID *uuid.UUID // nil = all hands for the helm
	After  time.Time  // zero = no lower bound
	Before time.Time  // zero = no upper bound
	Limit  int        // 0 = default (100)
}
