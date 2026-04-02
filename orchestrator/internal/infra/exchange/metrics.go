package exchange

import (
	"context"
	"sync/atomic"
	"time"
)

// ActionMetrics tracks latency and error counts for a single exchange action type.
// All fields are atomic — safe for concurrent updates across goroutines.
type ActionMetrics struct {
	Calls      atomic.Int64 // total calls made
	Errors     atomic.Int64 // calls that returned a non-nil error
	TotalNanos atomic.Int64 // sum of call durations in nanoseconds
	MinNanos   atomic.Int64 // minimum single-call duration (0 = never observed)
	MaxNanos   atomic.Int64 // maximum single-call duration
}

// AvgLatency returns the mean call duration. Returns 0 if no calls yet.
func (m *ActionMetrics) AvgLatency() time.Duration {
	calls := m.Calls.Load()
	if calls == 0 {
		return 0
	}
	return time.Duration(m.TotalNanos.Load() / calls)
}

// ErrorRate returns errors / calls. Returns 0 if no calls yet.
func (m *ActionMetrics) ErrorRate() float64 {
	calls := m.Calls.Load()
	if calls == 0 {
		return 0
	}
	return float64(m.Errors.Load()) / float64(calls)
}

func (m *ActionMetrics) record(d time.Duration, err error) {
	ns := d.Nanoseconds()
	m.Calls.Add(1)
	m.TotalNanos.Add(ns)
	if err != nil {
		m.Errors.Add(1)
	}
	// Update min atomically (CAS loop).
	for {
		cur := m.MinNanos.Load()
		if cur != 0 && cur <= ns {
			break
		}
		if m.MinNanos.CompareAndSwap(cur, ns) {
			break
		}
	}
	// Update max atomically.
	for {
		cur := m.MaxNanos.Load()
		if cur >= ns {
			break
		}
		if m.MaxNanos.CompareAndSwap(cur, ns) {
			break
		}
	}
}

// ExchangeMetrics holds per-action metrics for one exchange adapter instance.
// Embed or attach to an exchange adapter to expose infrastructure health.
type ExchangeMetrics struct {
	PlaceOrder  ActionMetrics
	GetOrder    ActionMetrics
	CancelOrder ActionMetrics
	Ping        ActionMetrics // optional — populated by MeteredExchange.Ping()

	// PingLastNanos is the most recent ping RTT in nanoseconds (0 = never measured).
	PingLastNanos atomic.Int64
}

// ── MeteredExchange ───────────────────────────────────────────────────────────

// MeteredExchange wraps any Exchange and records ExchangeMetrics for each call.
// It satisfies the Exchange interface and is transparent to callers.
type MeteredExchange struct {
	inner   Exchange
	Metrics ExchangeMetrics
}

// NewMeteredExchange wraps inner with latency/error instrumentation.
func NewMeteredExchange(inner Exchange) *MeteredExchange {
	return &MeteredExchange{inner: inner}
}

func (m *MeteredExchange) Name() string { return m.inner.Name() }

func (m *MeteredExchange) PlaceOrder(ctx context.Context, req OrderRequest) (*OrderResult, error) {
	start := time.Now()
	res, err := m.inner.PlaceOrder(ctx, req)
	m.Metrics.PlaceOrder.record(time.Since(start), err)
	return res, err
}

func (m *MeteredExchange) GetOrder(ctx context.Context, orderID string) (*OrderResult, error) {
	start := time.Now()
	res, err := m.inner.GetOrder(ctx, orderID)
	m.Metrics.GetOrder.record(time.Since(start), err)
	return res, err
}

func (m *MeteredExchange) CancelOrder(ctx context.Context, orderID string) error {
	start := time.Now()
	err := m.inner.CancelOrder(ctx, orderID)
	m.Metrics.CancelOrder.record(time.Since(start), err)
	return err
}

// Ping measures a round-trip to the exchange by calling GetOrder with a dummy ID
// and recording only the latency (errors are expected and ignored for ping purposes).
// For exchanges that expose a dedicated server-time or ping endpoint, override this.
func (m *MeteredExchange) Ping(ctx context.Context) time.Duration {
	start := time.Now()
	_, _ = m.inner.GetOrder(ctx, "__ping__")
	rtt := time.Since(start)
	ns := rtt.Nanoseconds()
	m.Metrics.Ping.Calls.Add(1)
	m.Metrics.Ping.TotalNanos.Add(ns)
	m.Metrics.PingLastNanos.Store(ns)
	return rtt
}

// Snapshot returns a point-in-time copy of all action metrics, safe to serialize.
type MetricsSnapshot struct {
	Name        string         `json:"exchange"`
	PlaceOrder  ActionSnapshot `json:"place_order"`
	GetOrder    ActionSnapshot `json:"get_order"`
	CancelOrder ActionSnapshot `json:"cancel_order"`
	PingLastMs  float64        `json:"ping_last_ms"`
}

type ActionSnapshot struct {
	Calls     int64   `json:"calls"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	AvgMs     float64 `json:"avg_ms"`
	MinMs     float64 `json:"min_ms"`
	MaxMs     float64 `json:"max_ms"`
}

func snapshotAction(a *ActionMetrics) ActionSnapshot {
	calls := a.Calls.Load()
	errors := a.Errors.Load()
	totalNs := a.TotalNanos.Load()
	minNs := a.MinNanos.Load()
	maxNs := a.MaxNanos.Load()

	s := ActionSnapshot{
		Calls:  calls,
		Errors: errors,
	}
	if calls > 0 {
		s.ErrorRate = float64(errors) / float64(calls)
		s.AvgMs = float64(totalNs/calls) / 1e6
		s.MinMs = float64(minNs) / 1e6
		s.MaxMs = float64(maxNs) / 1e6
	}
	return s
}

// Snapshot returns a serializable point-in-time copy of all metrics.
func (m *MeteredExchange) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		Name:        m.Name(),
		PlaceOrder:  snapshotAction(&m.Metrics.PlaceOrder),
		GetOrder:    snapshotAction(&m.Metrics.GetOrder),
		CancelOrder: snapshotAction(&m.Metrics.CancelOrder),
		PingLastMs:  float64(m.Metrics.PingLastNanos.Load()) / 1e6,
	}
}

// ensure MeteredExchange satisfies Exchange at compile time.
var _ Exchange = (*MeteredExchange)(nil)

// AtomicMinStore atomically stores v only if v < current (or current == 0).
// Exported for testing.
func AtomicMinStore(target *atomic.Int64, v int64) {
	for {
		cur := target.Load()
		if cur != 0 && cur <= v {
			return
		}
		if target.CompareAndSwap(cur, v) {
			return
		}
	}
}
