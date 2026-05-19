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
