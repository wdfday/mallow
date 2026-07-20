// Package poslog defines the position event log contract.
//
// Every state-changing action on a hand's position leg is recorded as an
// append-only event to a durable JetStream stream. On restart, replaying
// all events for a hand fully reconstructs its position state.
//
// Stream:   HELM_POSITIONS
// Subjects: helm.pos.{helm_id}.{hand_id}.{position_id}
//
// position_id = opening order_id of the leg — the wire/JSON field is kept as
// "position_id" for JetStream replay compatibility with events persisted
// before Event.TradeID was renamed from PositionID; the Go-side name is
// TradeID. For pyramid mode this is stable across all adds (all events go to
// the same subject). For non-pyramid mode each independent leg has its own
// subject.
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
	// KindOrderPlace is written before submitting the order to the exchange (pre-flight).
	// Represents intent to trade with client_order_id.
	KindOrderPlace Kind = "order_place"

	// KindOrderPlaced is written immediately after the exchange returns an order_id.
	// IsPyramidAdd=false → opening a new leg.
	// IsPyramidAdd=true  → adding to an existing pyramid position.
	// IsClose=true        → placing a close order on the leg.
	KindOrderPlaced Kind = "order_placed"

	// KindOrderFilled is written when the exchange confirms a full fill.
	// For pyramid adds: AvgEntryPrice and TotalQty are updated; SL/TP come from
	// the preceding KindOrderPlaced (already in the stream).
	KindOrderFilled Kind = "order_filled"

	// KindOrderCancelled is written when an order is canceled or rejected.
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

	// KindBracketPlace is written BEFORE submitting SL/TP bracket orders to the
	// exchange (pre-flight) — mirrors KindOrderPlace for the main order. Without
	// this, a crash between the PlaceExitOrders REST call and KindBracketPlaced
	// leaves no record a bracket was ever attempted, so restart can't tell
	// "never sent" from "sent but the response never came back" — it just sees
	// an unprotected leg. ClientOrderID is what a restart queries the exchange
	// by to resolve which of those actually happened.
	KindBracketPlace Kind = "bracket_place"

	// KindBracketPlaced is written after exchange-side SL/TP bracket orders are placed
	// successfully. Persists the order IDs so cancelExitOrders and checkBracketOrders
	// can cancel the sibling after a restart.
	KindBracketPlaced Kind = "bracket_placed"
)

// ExitReason classifies why a position closed — the "source" field on
// PositionClosedPayload. Wire values are unchanged from before this type
// existed; this only adds compile-time-checked names for the literals every
// call site used inline. Only the reasons determined at fill-classification
// time (hand_fills.go's applyExitFill, reconcile.go's tryRecoverBracketFill)
// are named here — "kill" / "release" / "external" / "bracket_exit" stay
// untyped string literals at their call sites (cast to ExitReason at the
// point they're written into PositionClosedPayload). Orphan never reaches
// this payload at all (see KindPositionOrphaned/PositionOrphanedPayload) so
// it has no ExitReason constant — see strategy.ExitKindOrphan instead.
type ExitReason string

const (
	ExitReasonTakeProfit ExitReason = "tp"
	ExitReasonStopLoss   ExitReason = "sl"
	// ExitReasonSignal is a plain strategy/NATS-originated exit (not a bracket
	// fill, not kill/release/external) — matches strategy.ExitKindSignal.
	ExitReasonSignal ExitReason = "signal"
)

// OrderPlacePayload carries the pre-flight order intent.
type OrderPlacePayload struct {
	ClientOrderID string `json:"client_order_id"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`       // "buy" | "sell"
	Qty           string `json:"qty"`        // decimal string
	Price         string `json:"price"`      // "0" for market orders
	OrderType     string `json:"order_type"` // "market" | "limit"
	StopLoss      string `json:"stop_loss,omitempty"`
	TakeProfit    string `json:"take_profit,omitempty"`
	IsPyramidAdd  bool   `json:"is_pyramid_add,omitempty"` // true → adding to existing leg
	IsClose       bool   `json:"is_close,omitempty"`       // true → closing the leg
}

// OrderPlacedPayload carries order intent. SL/TP here are the signal's values —
// for pyramid adds they will replace the existing leg's levels on fill.
type OrderPlacedPayload struct {
	OrderID string `json:"order_id"`
	// ClientOrderID is the mallow-generated clOrdId (empty for bracket/legacy orders).
	// The orders persister keys the orders table on this when present. See CLIENT_ORDER_ID.md.
	ClientOrderID string `json:"client_order_id,omitempty"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`       // "buy" | "sell"
	Qty           string `json:"qty"`        // decimal string
	Price         string `json:"price"`      // "0" for market orders
	OrderType     string `json:"order_type"` // "market" | "limit"
	StopLoss      string `json:"stop_loss,omitempty"`
	TakeProfit    string `json:"take_profit,omitempty"`
	IsPyramidAdd  bool   `json:"is_pyramid_add,omitempty"` // true → adding to existing leg
	IsClose       bool   `json:"is_close,omitempty"`       // true → closing the leg
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
	// DeployedCapital is the quote cost of this fill: net_qty×price + entry_fee_quote.
	// Accumulated into LegState so net PnL can be computed as received − deployed.
	// Zero on old events (backward-compat: those trades will have imprecise net PnL).
	DeployedCapital string `json:"deployed_capital,omitempty"`
}

// OrderCancelledPayload describes why an order did not result in a fill.
type OrderCancelledPayload struct {
	OrderID string `json:"order_id"`
	// Reason: "cancelled" | "rejected" | "expired" | "external"
	Reason string `json:"reason"`
}

// SLUpdatedPayload records a stop-loss or take-profit level change on an open leg.
type SLUpdatedPayload struct {
	OrderID string `json:"order_id"` // opening order_id of the leg (= TradeID)
	NewSL   string `json:"new_sl"`
	NewTP   string `json:"new_tp,omitempty"`
	Reason  string `json:"reason"` // "trailing" | "manual"
}

// BracketPlacedPayload records the exchange order IDs for SL/TP bracket orders placed
// after an entry fill. Replayed on restart to restore ExchangeOrderIDs in exitLevels.
type BracketPlacedPayload struct {
	Symbol   string   `json:"symbol"`
	OrderIDs []string `json:"order_ids"`
	// ClientOrderID is the clid this bracket was placed with (matches the
	// preceding KindBracketPlace event's payload); empty for adapters that
	// don't support one.
	ClientOrderID string `json:"client_order_id,omitempty"`
	// GroupID is the exchange-side identifier for the whole bracket group
	// (Binance orderListId, OKX algoId) — lets cancelExitOrders cancel every
	// leg with one atomic call instead of OrderIDs one at a time. Empty for
	// adapters with no true exchange-side group (Bybit, fbinance, Alpaca) and
	// for old events persisted before this field existed.
	GroupID string `json:"group_id,omitempty"`
}

// BracketPlacePayload carries the pre-flight bracket-order intent — written
// before the exchange call, so a restart can discover an in-flight bracket
// placement (see KindBracketPlace).
type BracketPlacePayload struct {
	Symbol        string `json:"symbol"`
	ClientOrderID string `json:"client_order_id"`
	StopLoss      string `json:"stop_loss,omitempty"`
	TakeProfit    string `json:"take_profit,omitempty"`
	Qty           string `json:"qty"`
}

// PositionOrphanedPayload records that a hand released a leg without closing it.
type PositionOrphanedPayload struct {
	Symbol string `json:"symbol"`
	// Source: "release" | "manual"
	Source string `json:"source"`
}

// PositionClosedPayload records the final exit of a leg.
// All trade-relevant fields are stored here so the event is self-contained
// and can be used for paged trade history queries without replaying entry events.
type PositionClosedPayload struct {
	OrderID     string    `json:"order_id"` // opening order_id of the leg
	Symbol      string    `json:"symbol"`
	Side        string    `json:"side"`        // entry side: "buy" | "sell"
	Qty         string    `json:"qty"`         // filled qty
	EntryPrice  string    `json:"entry_price"` // avg entry price
	EntryAt     time.Time `json:"entry_at"`
	ClosePrice  string    `json:"close_price"`
	RealizedPnL string    `json:"realized_pnl"`
	// ExitReason: ExitReasonSignal | ExitReasonStopLoss | ExitReasonTakeProfit |
	// "external" | "kill" | "release" | "bracket_exit" (the last four are untyped
	// literals — see the ExitReason type doc for why).
	// (Wire tag kept as "source" for backward compat with already-published events.)
	ExitReason ExitReason `json:"source"`
	// StopLoss / TakeProfit prices active at time of close (zero = none).
	StopLossPrice   string `json:"stop_loss_price,omitempty"`
	TakeProfitPrice string `json:"take_profit_price,omitempty"`
	// EntryOrderID = leg's opening order_id (= TradeID).
	// ExitOrderID  = the closing fill's order_id.
	EntryOrderID string `json:"entry_order_id,omitempty"`
	ExitOrderID  string `json:"exit_order_id,omitempty"`
	// NEntries: how many fills opened/added to the leg (1 for non-pyramid).
	NEntries int `json:"n_entries,omitempty"`
	// Commission paid on the closing fill (may be zero when exchange doesn't report it).
	Commission string `json:"commission,omitempty"`
	// DeployedCapital is the total quote spent across all entry fills for this leg.
	// Populated from LegState at close time. Used for net_pnl = received - deployed.
	DeployedCapital string `json:"deployed_capital,omitempty"`
}

// Event is one entry in the position log.
type Event struct {
	// ID is the dedup key for JetStream idempotent publish.
	// Convention: order_id for KindOrderPlaced; order_id+"_filled" for KindOrderFilled; etc.
	ID      string    `json:"id"`
	HandID  string    `json:"hand_id"`
	HelmID  string    `json:"helm_id"`
	TradeID string    `json:"position_id"` // = opening order_id of the leg
	Kind    Kind      `json:"kind"`
	Payload []byte    `json:"payload"` // JSON-encoded kind-specific struct above
	At      time.Time `json:"at"`
}

// TradeRecord is a completed round-trip trade reconstructed from a position_closed event.
// All fields are sourced from PositionClosedPayload (self-contained since v2).
// Old events (pre-enrichment) will have empty symbol/side/qty/entry fields.
type TradeRecord struct {
	HandID      string    `json:"hand_id"`
	Symbol      string    `json:"symbol"`
	Side        string    `json:"side"`
	Qty         string    `json:"qty"`
	EntryPrice  string    `json:"entry_price"`
	ExitPrice   string    `json:"exit_price"`
	EntryAt     time.Time `json:"entry_at"`
	ExitAt      time.Time `json:"exit_at"`
	RealizedPnL string    `json:"realized_pnl"`
	Source      string    `json:"source"`
	Cursor      uint64    `json:"cursor"` // JetStream sequence of this event; use as next-page cursor
}

// TradesPage is the result of a paged trade query.
type TradesPage struct {
	Trades  []TradeRecord `json:"trades"`
	Next    uint64        `json:"next"` // pass as cursor on next call; 0 = no more pages
	HasMore bool          `json:"has_more"`
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
	ReplayLeg(ctx context.Context, helmID, handID, tradeID string) ([]Event, error)

	// TradesPaged returns at most limit completed trades for a hand, ordered by close time.
	// cursor=0 starts from the beginning of the stream; pass TradeRecord.Cursor+1 for the
	// next page. Only position_closed events are returned — other event kinds are skipped.
	TradesPaged(ctx context.Context, helmID, handID string, cursor uint64, limit int) (TradesPage, error)

	// PurgeHand deletes all poslog messages for a hand from the JetStream stream.
	// Called after a hand reaches flat (all legs closed/orphaned) so the stream stays
	// lean and ReplayHand on the next restart returns an empty slice — the hand
	// restarts clean without re-processing stale closed-position events.
	// Safe to call even if already empty (NATS treats it as a no-op).
	PurgeHand(ctx context.Context, helmID, handID string) error
}
