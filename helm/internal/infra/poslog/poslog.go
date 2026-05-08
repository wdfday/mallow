// Package poslog defines the position event log contract.
//
// Every state-changing action on a hand's position leg is recorded as an
// append-only event to a durable JetStream stream. On restart, replaying
// all events for a hand fully reconstructs its position state.
//
// Stream:   HELM_POSITIONS
// Subjects: helm.pos.{helm_id}.{hand_id}.{position_id}
//
// position_id = opening order_id of the leg. For pyramid mode this is stable
// across all adds (all events go to the same subject). For non-pyramid mode
// each independent leg has its own subject.
//
// Dedup:    Nats-Msg-Id = Event.ID  (order_id + kind suffix)
package poslog

import (
	"context"
	"time"
)

// Kind identifies the type of position event.
type Kind string

const (
	// KindOrderPlaced is written immediately after the exchange returns an order_id.
	// IsPyramidAdd=false → opening a new leg.
	// IsPyramidAdd=true  → adding to an existing pyramid position.
	// IsClose=true        → placing a close order on the leg.
	KindOrderPlaced Kind = "order_placed"

	// KindOrderFilled is written when the exchange confirms a full fill.
	// For pyramid adds: AvgEntryPrice and TotalQty are updated; SL/TP come from
	// the preceding KindOrderPlaced (already in the stream).
	KindOrderFilled Kind = "order_filled"

	// KindOrderCancelled is written when an order is cancelled or rejected.
	// Phase reverts: Entering→Idle, Adding→Open, Exiting→Open.
	KindOrderCancelled Kind = "order_cancelled"

	// KindSLUpdated is written when trailing stop or SL level changes on an open leg.
	KindSLUpdated Kind = "sl_updated"

	// KindPositionClosed is written when a leg is fully exited.
	// Source distinguishes intentional exit from external close (liquidation, manual).
	KindPositionClosed Kind = "position_closed"

	// KindPositionOrphaned is written when a hand releases a leg without closing it.
	// The position remains open at the exchange but is no longer tracked by any hand.
	// On replay, this event removes the leg from HandPositions so the hand never
	// reclaims it on restart.
	KindPositionOrphaned Kind = "position_orphaned"
)

// OrderPlacedPayload carries order intent. SL/TP here are the signal's values —
// for pyramid adds they will replace the existing leg's levels on fill.
type OrderPlacedPayload struct {
	OrderID      string `json:"order_id"`
	Symbol       string `json:"symbol"`
	Side         string `json:"side"`       // "buy" | "sell"
	Qty          string `json:"qty"`        // decimal string
	Price        string `json:"price"`      // "0" for market orders
	OrderType    string `json:"order_type"` // "market" | "limit"
	StopLoss     string `json:"stop_loss,omitempty"`
	TakeProfit   string `json:"take_profit,omitempty"`
	IsPyramidAdd bool   `json:"is_pyramid_add,omitempty"` // true → adding to existing leg
	IsClose      bool   `json:"is_close,omitempty"`       // true → closing the leg
}

// OrderFilledPayload carries fill confirmation. For pyramid adds, SL/TP are
// taken from the corresponding OrderPlacedPayload already in the stream.
type OrderFilledPayload struct {
	OrderID   string `json:"order_id"`
	FillPrice string `json:"fill_price"`
	FillQty   string `json:"fill_qty"`
	// Source identifies how the fill was detected.
	// "ws" = WebSocket push, "poll" = periodic poll, "reconcile" = startup reconcile.
	Source string `json:"source"`
}

// OrderCancelledPayload describes why an order did not result in a fill.
type OrderCancelledPayload struct {
	OrderID string `json:"order_id"`
	// Reason: "cancelled" | "rejected" | "expired" | "external"
	Reason string `json:"reason"`
}

// SLUpdatedPayload records a stop-loss or take-profit level change on an open leg.
type SLUpdatedPayload struct {
	OrderID string `json:"order_id"` // opening order_id of the leg (= PositionID)
	NewSL   string `json:"new_sl"`
	NewTP   string `json:"new_tp,omitempty"`
	Reason  string `json:"reason"` // "trailing" | "manual"
}

// PositionOrphanedPayload records that a hand released a leg without closing it.
type PositionOrphanedPayload struct {
	Symbol string `json:"symbol"`
	// Source: "release" | "manual"
	Source string `json:"source"`
}

// PositionClosedPayload records the final exit of a leg.
type PositionClosedPayload struct {
	OrderID     string `json:"order_id"` // opening order_id of the leg
	ClosePrice  string `json:"close_price"`
	RealizedPnL string `json:"realized_pnl"`
	// Source: "signal" | "sl" | "tp" | "time_stop" | "external" | "kill"
	Source string `json:"source"`
}

// Event is one entry in the position log.
type Event struct {
	// ID is the dedup key for JetStream idempotent publish.
	// Convention: order_id for KindOrderPlaced; order_id+"_filled" for KindOrderFilled; etc.
	ID         string    `json:"id"`
	HandID     string    `json:"hand_id"`
	HelmID     string    `json:"helm_id"`
	PositionID string    `json:"position_id"` // = opening order_id of the leg
	Kind       Kind      `json:"kind"`
	Payload    []byte    `json:"payload"` // JSON-encoded kind-specific struct above
	At         time.Time `json:"at"`
}

// Log is the write/read contract for the position event log.
type Log interface {
	// Publish appends an event. Idempotent: duplicate Event.ID within the JetStream
	// dedup window (default 2 min) is silently discarded.
	Publish(ctx context.Context, e Event) error

	// ReplayHand returns all events for a hand across all its position legs, in order.
	// Used on startup to reconstruct HandPositions.
	ReplayHand(ctx context.Context, helmID, handID string) ([]Event, error)

	// ReplayLeg returns events for a single position leg.
	// position_id = opening order_id of the leg.
	ReplayLeg(ctx context.Context, helmID, handID, positionID string) ([]Event, error)
}
