package ws

import (
	"encoding/binary"
	"encoding/json"
)

// ── Client → server control messages (JSON text frames) ──────────────────────

// clientMsg is a control message sent by the client over the WebSocket.
type clientMsg struct {
	Op     string            `json:"op"`               // hello | subscribe | unsubscribe | ping
	Ch     string            `json:"ch,omitempty"`     // market channel: "bars"
	Symbol string            `json:"symbol,omitempty"` // for bars subscribe/unsubscribe
	TF     string            `json:"tf,omitempty"`     // informational; herald publishes one TF per instance
	Resume map[string]uint64 `json:"resume,omitempty"` // account ch → last stream seq the client processed
}

// ── Server → client account/control messages (JSON text frames) ──────────────

// accountEnvelope wraps a scoped account event. seq is the JetStream stream
// sequence so the client can resume from it after a reconnect.
type accountEnvelope struct {
	Ch   string          `json:"ch"`   // helm | trade | portfolio
	Key  string          `json:"key"`  // helm_id or account_id (last subject token)
	Seq  uint64          `json:"seq"`  // JetStream stream sequence
	Data json.RawMessage `json:"data"` // raw event payload, untouched
}

// ctrlEnvelope is an ack or error reply to a client control message.
type ctrlEnvelope struct {
	Ch   string `json:"ch"`             // "ack" | "error"
	Op   string `json:"op,omitempty"`   // echoed op for ack
	Key  string `json:"key,omitempty"`  // echoed key (e.g. "BTCUSDT")
	OK   bool   `json:"ok,omitempty"`   // ack only
	Code string `json:"code,omitempty"` // error only
	Msg  string `json:"msg,omitempty"`  // error only
}

func ack(op, key string) []byte {
	b, _ := json.Marshal(ctrlEnvelope{Ch: "ack", Op: op, Key: key, OK: true})
	return b
}

func wsError(code, msg string) []byte {
	b, _ := json.Marshal(ctrlEnvelope{Ch: "error", Code: code, Msg: msg})
	return b
}

// ── Server → client market frames (binary) ───────────────────────────────────

// market channel tags for the binary frame header.
const (
	tagBars     byte = 0x01 // closed bar (bars.{sym})
	tagBarsLive byte = 0x02 // forming bar (bars.live.{sym}) — current candle, not final
)

// encodeMarketFrame builds a binary market frame:
//
//	[1 byte ch-tag][2 byte big-endian key length][key utf8][raw protobuf payload]
//
// The client distinguishes market (binary frame) from account/control (text
// frame) by the WebSocket opcode, then reads the tag + key to demux.
func encodeMarketFrame(tag byte, key string, payload []byte) []byte {
	out := make([]byte, 0, 3+len(key)+len(payload))
	out = append(out, tag)
	var klen [2]byte
	binary.BigEndian.PutUint16(klen[:], uint16(len(key)))
	out = append(out, klen[:]...)
	out = append(out, key...)
	out = append(out, payload...)
	return out
}
