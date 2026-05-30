package readmodel

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// OrderRecord is one row of the `orders` read model — the durable lifecycle history
// of a single order (placed → filled | cancelled), projected from the poslog by the
// orders persister. Distinct from the in-memory runtime order list (live orders only).
type OrderRecord struct {
	ExchangeOrderID string
	ClientOrderID   string
	HelmID          uuid.UUID
	HandID          string // may be empty / "manual" for orphan orders
	PositionID      string
	Symbol          string
	Side            string
	OrderType       string
	Qty             decimal.Decimal
	Price           decimal.Decimal
	Status          string // placed | filled | cancelled
	FilledQty       decimal.Decimal
	FilledPrice     decimal.Decimal
	IsClose         bool
	PatternKind     string
	Reason          string
	PlacedAt        time.Time
	UpdatedAt       time.Time
}

// OrderFilter constrains an orders Query. HelmID is required (orders are helm-scoped);
// HandID / Status / time bounds are optional.
type OrderFilter struct {
	HelmID uuid.UUID
	HandID string
	Status string
	After  time.Time
	Before time.Time
	Limit  int
}
