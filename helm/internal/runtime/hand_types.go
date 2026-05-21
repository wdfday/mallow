package runtime

import (
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/runtime/core/strategy"
)

// Signal is the canonical decoded signal type used throughout the runtime.
// All fields come from the herald protobuf SignalMsg + NATS metadata.
type Signal = strategy.Signal

// Health status values for HandHealth.Status.
// The first three mirror domain.HandStatus for the persisted lifecycle states.
// The remainder are runtime-only states not written to the DB.
const (
	HealthRunning  = "running"
	HealthStopped  = "stopped"
	HealthPaused   = "paused"
	HealthStale    = "stale"    // no signal received for >5 min; run-loop still active
	HealthError    = "error"    // last order failed; clears on next successful order
	HealthKilled   = "killed"   // Kill() called; positions flattened
	HealthReleased = "released" // Release() called; positions orphaned
)

// HandHealth tracks liveness and error state.
type HandHealth struct {
	Status       string     `json:"status"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	LastSignalAt *time.Time `json:"last_signal_at,omitempty"`
	LastOrderAt  *time.Time `json:"last_order_at,omitempty"`
	LastErrorAt  *time.Time `json:"last_error_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Uptime       string     `json:"uptime,omitempty"`
}

// ── Activity codes ────────────────────────────────────────────────────────────
//
// Numeric event codes for the hand activity log and NATS HelmEvent stream.
// Ranges:
//   10000–10099  signal lifecycle
//   10100–10199  order lifecycle
//   10200–10299  hand lifecycle
//   10300–10399  helm lifecycle

const (
	// Signal received — logged for every signal before any filtering.
	CodeSignalReceived = 10000

	// Signal filtered codes — signal was received but not acted on.
	CodeSignalStale       = 10001 // arrived more than signalMaxAge after dispatch
	CodeSignalHelmPaused  = 10002 // helm-level cascade pause active
	CodeSignalHandPaused  = 10003 // this hand is individually paused
	CodeSignalRateLimited = 10004 // exceeded per-second order rate limit
	CodeSignalDoNothing   = 10005 // strategy evaluated to no-action (e.g. strength below min)
	CodeSignalMaxUnits    = 10006 // position count already at maxUnits cap
	CodeSignalRejected    = 10007 // ProcessTrade rejected (risk, capital, duplicate, etc.)
	CodeSignalNoPosition  = 10008 // urgent exit dropped: position already closed (OCO race guard)

	// Order lifecycle codes.
	CodeOrderPlaced        = 10100 // order successfully submitted to exchange
	CodeOrderFilled        = 10101 // order confirmed filled (via WS or poll)
	CodeOrderFailed        = 10102 // exchange returned an error for the order
	CodeOrderPartialCancel = 10103 // partial fill remainder auto-cancelled (below min lot)
	CodeOrderLimitTimeout  = 10104 // limit order cancelled by helm after timeout with no fill
	CodeOrderLimitReprice  = 10105 // reserved: cancel + re-price (not yet implemented; see EXECUTION_TACTICS.md)
	CodeOrderLimitFallback = 10106 // limit order cancelled and re-placed as market after timeout
	CodeOrderCancelled     = 10107 // order cancelled / rejected / expired (detected via poll)
	CodeOrderExitTriggered = 10108 // local stop-loss or take-profit safety net triggered
	CodeOrderExitPlaced    = 10109 // safety net exit orders (SL/TP bracket) submitted to exchange

	// Hand lifecycle codes.
	CodeHandAutoPaused  = 10200 // hand auto-paused due to persistent sizing failure or edge risk
	CodeHandStarted     = 10201 // hand run-loop started
	CodeHandStopped     = 10202 // hand run-loop stopped (clean shutdown)
	CodeHandPaused      = 10203 // hand manually paused via API
	CodeHandResumed     = 10204 // hand manually resumed via API
	CodeHandKilled      = 10205 // hand killed — all positions flattened at market
	CodeHandReleased    = 10206 // hand released — open positions orphaned (left live at exchange)
	CodeHandStale       = 10207 // signal feed silent > stale threshold
	CodeHandLeverageSet = 10208 // futures leverage and margin type configured at exchange

	// Helm lifecycle codes (used in HelmRuntime activity ring).
	CodeHelmPaused   = 10300 // helm paused — all hands will ignore signals
	CodeHelmResumed  = 10301 // helm resumed
	CodeHelmSynced   = 10302 // portfolio synced from exchange
	CodeHelmHalted   = 10303 // helm halted by risk manager
	CodeHelmUnhalted = 10304 // helm halt reset (manual)
)

// ActivityEntry records a single hand event in the activity ring buffer.
type ActivityEntry struct {
	At        time.Time       `json:"at"`
	Code      int             `json:"code"`
	Symbol    string          `json:"symbol"`
	Direction string          `json:"direction,omitempty"` // signal direction (long/short/close/…)
	Strength  float64         `json:"strength,omitempty"`
	Reason    string          `json:"reason,omitempty"` // human-readable detail (filter cause, rejection message, error)
	OrderID   string          `json:"order_id,omitempty"`
	Side      string          `json:"side,omitempty"`
	Qty       decimal.Decimal `json:"qty,omitempty"`
	Price     decimal.Decimal `json:"price,omitempty"`
}

// activityRingSize is the number of entries retained in the in-memory activity ring.
// Small on purpose — the durable event log lives in HELM_EVENTS JetStream;
// the ring is only for test observability and the recent-events UI chip.
const activityRingSize = 20

// ActivityRing is a fixed-size circular buffer for recent hand activity entries.
// Thread-safe via an embedded mutex. Oldest entries are silently overwritten.
type ActivityRing struct {
	mu   sync.Mutex
	buf  [activityRingSize]ActivityEntry
	head int // next write slot
	size int // entries written so far (capped at activityRingSize)
}

// push appends an entry, overwriting the oldest when full.
func (r *ActivityRing) push(e ActivityEntry) {
	r.mu.Lock()
	r.buf[r.head] = e
	r.head = (r.head + 1) % activityRingSize
	if r.size < activityRingSize {
		r.size++
	}
	r.mu.Unlock()
}

// Snapshot returns all entries in chronological order (oldest first).
func (r *ActivityRing) Snapshot() []ActivityEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size == 0 {
		return nil
	}
	out := make([]ActivityEntry, r.size)
	if r.size < activityRingSize {
		copy(out, r.buf[:r.size])
	} else {
		n := copy(out, r.buf[r.head:])
		copy(out[n:], r.buf[:r.head])
	}
	return out
}
