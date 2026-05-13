package engine

// Re-export generated proto types for use within this package.
// All signal-engine ↔ helm communication uses protobuf over NATS.

import market "mallow/helm/internal/proto"

// Type aliases — callers import only the engine package.
type (
	BarMsg            = market.BarMsg
	SignalMsg         = market.SignalMsg
	SignalResponse    = market.SignalResponse
	ResetMsg          = market.ResetMsg
	RegisterMsg       = market.RegisterMsg
	DeregisterMsg     = market.DeregisterMsg
	HandInfo          = market.HandInfo
	HandListResponse  = market.HandListResponse
	PingResponse      = market.PingResponse
	HeartbeatRequest  = market.HeartbeatRequest
	HeartbeatResponse = market.HeartbeatResponse
	ReadyEvent        = market.ReadyEvent
)

// AggregatedSignal is the in-process result of collapsing N hand signals for the
// same symbol and bar timestamp. Never serialized; consumed within helm.
type AggregatedSignal struct {
	Symbol    string
	Timestamp int64
	// Direction: "long" | "short" | "exit" | "none"
	Direction string
	// Strength [0,1] — weighted net of contributing signals.
	Strength float64
	// Sources: hand_ids that contributed to this aggregate.
	Sources []string
}
