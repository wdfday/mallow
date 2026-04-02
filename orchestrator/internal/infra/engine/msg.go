package engine

// Re-export generated proto types for use within this package.
// All signal-engine ↔ orchestrator communication uses protobuf over NATS.

import market "orchestrator/internal/proto"

// Type aliases — callers import only the engine package.
type (
	BarMsg          = market.BarMsg
	SignalMsg       = market.SignalMsg
	SignalResponse  = market.SignalResponse
	ConfigMsg       = market.ConfigMsg
	ResetMsg        = market.ResetMsg
	RegisterMsg     = market.RegisterMsg
	DeregisterMsg   = market.DeregisterMsg
	BotInfo         = market.BotInfo
	BotListResponse = market.BotListResponse
)

// AggregatedSignal is the in-process result of collapsing N bot signals for the
// same symbol and bar timestamp. Never serialized; consumed within orchestrator.
type AggregatedSignal struct {
	Symbol    string
	Timestamp int64
	// Direction: "long" | "short" | "close" | "none"
	Direction string
	// Strength [0,1] — weighted net of contributing signals.
	Strength float64
	// Sources: bot_ids that contributed to this aggregate.
	Sources []string
}
