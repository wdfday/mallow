package runtime

import (
	"sync"
	"time"

	"mallow/helm/internal/infra/natsapi"
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
	// 10003 reserved (was CodeSignalHandPaused — hand-level pause removed)
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
	CodeOrderDustExit      = 10110 // exit qty below exchange minimum — poslog closed without selling; dust stays at helm level

	// Hand lifecycle codes.
	CodeHandAutoStopped   = 10200 // hand auto-stopped due to persistent sizing failure or edge risk
	CodeHandStarted       = 10201 // hand run-loop started
	CodeHandStopped       = 10202 // hand run-loop stopped (clean shutdown)
	CodeHandKilled        = 10205 // hand killed — all positions flattened at market
	CodeHandReleased      = 10206 // hand released — open positions orphaned (left live at exchange)
	CodeHandStale         = 10207 // signal feed silent > stale threshold
	CodeHandLeverageSet   = 10208 // futures leverage and margin type configured at exchange
	CodePositionExtClosed = 10209 // position externally closed (user manual exit at exchange detected via bracket order cancel)

	// Helm lifecycle codes.
	CodeHelmPaused   = 10300 // helm paused — all hands will ignore signals
	CodeHelmResumed  = 10301 // helm resumed
	CodeHelmSynced   = 10302 // portfolio synced from exchange
	CodeHelmHalted   = 10303 // helm halted by risk manager
	CodeHelmUnhalted = 10304 // helm halt reset (manual)
)

// ── handEventBus — test-only broadcast ───────────────────────────────────────

// handEventBus is a simple fan-out for test observability.
// emitEvent broadcasts every HelmEvent to all registered subscriber channels.
// Nil-safe: all methods are no-ops when bus is nil (production path).
type handEventBus struct {
	mu   sync.Mutex
	subs []chan natsapi.HelmEvent
}

// subscribe returns a new channel that will receive all future events.
func (b *handEventBus) subscribe(cap int) <-chan natsapi.HelmEvent {
	ch := make(chan natsapi.HelmEvent, cap)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	return ch
}

// publish sends ev to every registered subscriber, non-blocking.
func (b *handEventBus) publish(ev natsapi.HelmEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default: // subscriber is slow; drop rather than block the run-loop
		}
	}
}
