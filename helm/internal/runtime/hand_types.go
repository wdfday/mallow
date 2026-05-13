package runtime

import (
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/core/strategy"
)

// Signal is the canonical decoded signal type used throughout the runtime.
// All fields come from the herald protobuf SignalMsg + NATS metadata.
type Signal = strategy.Signal

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
// Numeric event codes for the hand activity log.
// Ranges:
//   10000–10099  signal lifecycle
//   10100–10199  order lifecycle

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

	// Order lifecycle codes.
	CodeOrderPlaced = 10100 // order successfully submitted to exchange
	CodeOrderFilled = 10101 // order confirmed filled (via WS or poll)
	CodeOrderFailed = 10102 // exchange returned an error for the order
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

const activityRingSize = 200

// ActivityRing is a fixed-capacity circular buffer of ActivityEntry values.
// Safe for concurrent use. Oldest entries are overwritten when full.
type ActivityRing struct {
	mu      sync.Mutex
	entries [activityRingSize]ActivityEntry
	head    int
	count   int
}

func (r *ActivityRing) push(e ActivityEntry) {
	r.mu.Lock()
	r.entries[r.head] = e
	r.head = (r.head + 1) % activityRingSize
	if r.count < activityRingSize {
		r.count++
	}
	r.mu.Unlock()
}

// Snapshot returns a copy of all buffered entries in chronological order (oldest first).
func (r *ActivityRing) Snapshot() []ActivityEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.count == 0 {
		return nil
	}
	out := make([]ActivityEntry, r.count)
	start := (r.head - r.count + activityRingSize) % activityRingSize
	for i := 0; i < r.count; i++ {
		out[i] = r.entries[(start+i)%activityRingSize]
	}
	return out
}

// ── HandMetrics ───────────────────────────────────────────────────────────────

// HandMetrics tracks trading behavior counters.
type HandMetrics struct {
	SignalsReceived int64           `json:"signals_received"`
	SignalsFiltered int64           `json:"signals_filtered"`
	SignalsDropped  int64           `json:"signals_dropped"` // non-urgent signals lost due to full channel
	TradesApproved  int64           `json:"trades_approved"`
	OrdersPlaced    int64           `json:"orders_placed"`
	OrdersFilled    int64           `json:"orders_filled"`
	OrdersFailed    int64           `json:"orders_failed"`
	TotalPnL        decimal.Decimal `json:"total_pnl"`
	WinCount        int64           `json:"win_count"`
	LossCount       int64           `json:"loss_count"`
}

// ── HandRef ───────────────────────────────────────────────────────────────────

// HandRef is the runtime view of a hand: stored data + Hand runner + Exchange.
// Used by the service layer to bridge persistence and execution.
type HandRef struct {
	Data     *handdomain.Hand
	Runner   *Hand
	Exchange exchange.Exchange
}

// Summary returns a lightweight HandSummary from live runtime state.
func (b *HandRef) Summary() handdomain.HandSummary {
	h := b.Runner.Health()
	m := b.Runner.Metrics()

	hv := handdomain.HandHealthView{
		Status:    h.Status,
		LastError: h.LastError,
		Uptime:    h.Uptime,
	}
	if h.StartedAt != nil && !h.StartedAt.IsZero() {
		hv.StartedAt = h.StartedAt.Format("2006-01-02T15:04:05Z")
	}
	if h.LastSignalAt != nil && !h.LastSignalAt.IsZero() {
		hv.LastSignalAt = h.LastSignalAt.Format("2006-01-02T15:04:05Z")
	}
	if h.LastOrderAt != nil && !h.LastOrderAt.IsZero() {
		hv.LastOrderAt = h.LastOrderAt.Format("2006-01-02T15:04:05Z")
	}
	if h.LastErrorAt != nil && !h.LastErrorAt.IsZero() {
		hv.LastErrorAt = h.LastErrorAt.Format("2006-01-02T15:04:05Z")
	}

	mv := handdomain.HandMetricsView{
		SignalsReceived: m.SignalsReceived,
		SignalsFiltered: m.SignalsFiltered,
		TradesApproved:  m.TradesApproved,
		OrdersPlaced:    m.OrdersPlaced,
		OrdersFilled:    m.OrdersFilled,
		OrdersFailed:    m.OrdersFailed,
		TotalPnL:        m.TotalPnL,
		WinCount:        m.WinCount,
		LossCount:       m.LossCount,
	}

	return handdomain.HandSummary{
		ID:         b.Data.ID,
		Name:       b.Data.Name,
		Type:       b.Data.Type,
		Market:     b.Data.Market,
		HelmID:     b.Data.HelmID,
		Strategy:   b.Data.Strategy,
		Position:   b.Data.Position,
		Risk:       b.Data.Risk,
		Symbols:    []string(b.Data.Symbols),
		Status:     b.Data.Status,
		Running:    b.Runner.IsRunning(),
		OrderCount: len(b.Runner.Orders()),
		Health:     hv,
		Metrics:    mv,
		Futures:    b.Data.Futures,
		CreatedAt:  b.Data.CreatedAt,
	}
}
