package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Leg is one entry unit in a hand's position.
//
// Pyramid mode:   all signals merge into Legs[0]; PositionID stays constant.
// Non-pyramid:    each signal opens a new Leg with its own PositionID.
type Leg struct {
	PositionID string // = opening order_id; stable across pyramid adds
	EntryPrice decimal.Decimal
	Qty        decimal.Decimal
	StopLoss   decimal.Decimal // zero = not set
	TakeProfit decimal.Decimal // zero = not set
	OpenedAt   time.Time
}

// Position is the canonical value object for a hand's open exposure.
// Built from poslog state + current market price; never persisted directly.
//
// Pyramid mode  (Pyramid=true):  always len(Legs)==1; Legs[0] holds the merged state.
// Non-pyramid   (Pyramid=false): len(Legs) == number of open independent legs.
// Flat          (IsFlat=true):   len(Legs)==0, all fields are zero.
type Position struct {
	HandID  string
	HelmID  string
	Symbol  string
	Side    string // "buy" | "sell"; "" when flat
	Phase   string // "idle" | "entering" | "adding" | "open" | "exiting"
	Pyramid bool

	Legs []Leg // ordered oldest→newest

	// ── Aggregated (computed from Legs) ──────────────────────────────────────

	TotalQty      decimal.Decimal
	AvgEntryPrice decimal.Decimal

	// Pyramid: SL/TP from the latest signal (same for all units).
	// Non-pyramid: zero — each leg carries its own SL/TP via Legs[i].
	StopLoss   decimal.Decimal
	TakeProfit decimal.Decimal

	// ── Market ───────────────────────────────────────────────────────────────

	CurrentPrice  decimal.Decimal
	UnrealizedPnL decimal.Decimal // (CurrentPrice - AvgEntryPrice) × TotalQty; sign-aware for shorts
	MarketValue   decimal.Decimal // TotalQty × CurrentPrice

	// ── History ──────────────────────────────────────────────────────────────

	RealizedPnL decimal.Decimal // accumulated closed-trade PnL across hand's lifetime
	OpenedAt    time.Time       // first leg entry timestamp
	LastEntryAt time.Time       // latest leg entry timestamp (= OpenedAt for non-pyramid with 1 leg)
}

// Units returns the number of open legs.
func (p Position) Units() int { return len(p.Legs) }

// IsFlat reports whether the hand has no open exposure (no legs, no pending order).
func (p Position) IsFlat() bool { return len(p.Legs) == 0 && p.Phase == "idle" }

// NotionalRisk is the maximum loss if StopLoss is hit (for pyramid mode).
// For non-pyramid mode callers should sum Leg-level risks instead.
// Returns zero if StopLoss is not set.
func (p Position) NotionalRisk() decimal.Decimal {
	if p.StopLoss.IsZero() || p.AvgEntryPrice.IsZero() || p.TotalQty.IsZero() {
		return decimal.Zero
	}
	distance := p.AvgEntryPrice.Sub(p.StopLoss).Abs()
	return distance.Mul(p.TotalQty)
}

// RiskReward returns (TP − AvgEntry) / (AvgEntry − SL) for pyramid mode.
// Returns zero if either SL or TP is not set.
func (p Position) RiskReward() decimal.Decimal {
	if p.StopLoss.IsZero() || p.TakeProfit.IsZero() || p.AvgEntryPrice.IsZero() {
		return decimal.Zero
	}
	reward := p.TakeProfit.Sub(p.AvgEntryPrice).Abs()
	risk := p.AvgEntryPrice.Sub(p.StopLoss).Abs()
	if risk.IsZero() {
		return decimal.Zero
	}
	return reward.Div(risk)
}

// WithPrice returns a copy of the Position with market fields recomputed
// from the given current price. Does not mutate the receiver.
func (p Position) WithPrice(currentPrice decimal.Decimal) Position {
	p.CurrentPrice = currentPrice
	if !p.TotalQty.IsZero() && !p.AvgEntryPrice.IsZero() {
		diff := currentPrice.Sub(p.AvgEntryPrice)
		if p.Side == "sell" {
			diff = diff.Neg()
		}
		p.UnrealizedPnL = diff.Mul(p.TotalQty)
		p.MarketValue = p.TotalQty.Mul(currentPrice)
	}
	return p
}
