package exchange

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"
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

// WSMetrics tracks real-time WebSocket stream health.
// All fields are atomic — updated directly from the WS event callbacks.
type WSMetrics struct {
	// StreamStarts counts how many times StreamOrders was called
	// (each reconnect increments this).
	StreamStarts atomic.Int64
	// StreamErrors counts StreamOrders calls that returned a non-nil error.
	StreamErrors atomic.Int64

	// LifecycleEvents is the total number of order lifecycle events (ack/cancel)
	// received via WS.
	LifecycleEvents atomic.Int64
	// FillEvents is the total number of fill events (partial or full) received via WS.
	FillEvents atomic.Int64
	// BalanceEvents is the total number of balance-change events received via WS.
	BalanceEvents atomic.Int64

	// LastEventNanos is the Unix nanosecond timestamp of the most recent WS event
	// (order or balance). Zero means no event has arrived yet.
	LastEventNanos atomic.Int64
}

// IdleDuration returns the time elapsed since the last WS event.
// Returns 0 if no event has been received yet.
func (w *WSMetrics) IdleDuration() time.Duration {
	ns := w.LastEventNanos.Load()
	if ns == 0 {
		return 0
	}
	idle := time.Now().UnixNano() - ns
	if idle < 0 {
		return 0
	}
	return time.Duration(idle)
}

func (w *WSMetrics) touchEvent() {
	w.LastEventNanos.Store(time.Now().UnixNano())
}

// ExchangeMetrics holds per-action metrics for one exchange adapter instance.
type ExchangeMetrics struct {
	PlaceOrder  ActionMetrics
	GetOrder    ActionMetrics
	CancelOrder ActionMetrics
	SyncAccount ActionMetrics // REST portfolio sync latency
	Ping        ActionMetrics // HTTP ping (dummy GetOrder round-trip)

	// PingLastNanos is the most recent HTTP ping RTT in nanoseconds (0 = never measured).
	PingLastNanos atomic.Int64

	WS WSMetrics // WebSocket stream health

	// ErrorsByClass counts errors by class (ErrClassUnknown … ErrClassServerError).
	// Incremented by MeteredExchange on every non-nil error from PlaceOrder / GetOrder / CancelOrder.
	ErrorsByClass [ErrClassCount]atomic.Int64
}

// ── MeteredExchange ───────────────────────────────────────────────────────────

// MeteredExchange wraps any Exchange and records ExchangeMetrics for each call.
// It satisfies the Exchange interface and all optional interfaces — type assertions
// on the wrapped value reach the underlying adapter transparently.
type MeteredExchange struct {
	inner   Exchange
	Metrics ExchangeMetrics
}

// NewMeteredExchange wraps inner with latency/error and WS instrumentation.
func NewMeteredExchange(inner Exchange) *MeteredExchange {
	return &MeteredExchange{inner: inner}
}

func (m *MeteredExchange) Name() string { return m.inner.Name() }

// classifyErr uses the adapter's ErrorClassifier if available, otherwise ClassifyGeneric.
func (m *MeteredExchange) classifyErr(err error) ErrClass {
	if err == nil {
		return ErrClassUnknown
	}
	if c, ok := m.inner.(ErrorClassifier); ok {
		return c.ClassifyError(err)
	}
	return ClassifyGeneric(err)
}

func (m *MeteredExchange) recordErrClass(err error) {
	if err == nil {
		return
	}
	m.Metrics.ErrorsByClass[m.classifyErr(err)].Add(1)
}

// logExchangeErr emits a structured log entry for a non-nil exchange error.
// Level is error for severe classes (auth, server_error); warn for all recoverable ones.
// Fields are chosen so Loki queries like `| json | err_class="order_not_found"` work out of the box.
func (m *MeteredExchange) logExchangeErr(op, symbol, orderID string, err error) {
	cls := m.classifyErr(err)
	args := []any{
		"exchange", m.Name(),
		"op", op,
		"err_class", ErrClassName[cls],
		"err", err.Error(),
	}
	if symbol != "" {
		args = append(args, "symbol", symbol)
	}
	if orderID != "" {
		args = append(args, "order_id", orderID)
	}
	switch cls {
	case ErrClassAuth, ErrClassServerError:
		slog.Error("exchange error", args...)
	default:
		slog.Warn("exchange error", args...)
	}
}

func (m *MeteredExchange) PlaceOrder(ctx context.Context, creds Credentials, req OrderRequest) (*OrderResult, error) {
	start := time.Now()
	res, err := m.inner.PlaceOrder(ctx, creds, req)
	m.Metrics.PlaceOrder.record(time.Since(start), err)
	m.recordErrClass(err)
	if err != nil {
		m.logExchangeErr("place_order", req.Symbol, req.ClientOrderID, err)
	}
	return res, err
}

func (m *MeteredExchange) GetOrder(ctx context.Context, creds Credentials, orderID string) (*OrderResult, error) {
	start := time.Now()
	res, err := m.inner.GetOrder(ctx, creds, orderID)
	m.Metrics.GetOrder.record(time.Since(start), err)
	m.recordErrClass(err)
	if err != nil {
		m.logExchangeErr("get_order", "", orderID, err)
	}
	return res, err
}

func (m *MeteredExchange) CancelOrder(ctx context.Context, creds Credentials, orderID string) error {
	start := time.Now()
	err := m.inner.CancelOrder(ctx, creds, orderID)
	m.Metrics.CancelOrder.record(time.Since(start), err)
	m.recordErrClass(err)
	if err != nil {
		m.logExchangeErr("cancel_order", "", orderID, err)
	}
	return err
}

func (m *MeteredExchange) ListOpenOrders(ctx context.Context, creds Credentials, symbol string) ([]OrderResult, error) {
	return m.inner.ListOpenOrders(ctx, creds, symbol)
}

func (m *MeteredExchange) ListPositions(ctx context.Context, creds Credentials) ([]PositionResult, error) {
	return m.inner.ListPositions(ctx, creds)
}

// SubscribeFills implements FillStreamer — delegates to inner if it implements FillStreamer.
func (m *MeteredExchange) SubscribeFills(ctx context.Context, creds Credentials) (<-chan FillEvent, error) {
	if s, ok := m.inner.(FillStreamer); ok {
		return s.SubscribeFills(ctx, creds)
	}
	return nil, fmt.Errorf("exchange %q does not implement FillStreamer", m.Name())
}

// Ping measures HTTP round-trip latency to the exchange.
// Uses a dummy GetOrder call; errors are expected and ignored.
func (m *MeteredExchange) Ping(ctx context.Context) time.Duration {
	start := time.Now()
	_, _ = m.inner.GetOrder(ctx, Credentials{}, "__ping__")
	rtt := time.Since(start)
	ns := rtt.Nanoseconds()
	m.Metrics.Ping.Calls.Add(1)
	m.Metrics.Ping.TotalNanos.Add(ns)
	m.Metrics.PingLastNanos.Store(ns)
	return rtt
}

// ── Snapshot ──────────────────────────────────────────────────────────────────

// MetricsSnapshot is a serializable point-in-time copy of all exchange metrics.
type MetricsSnapshot struct {
	Name        string         `json:"exchange"`
	PlaceOrder  ActionSnapshot `json:"place_order"`
	GetOrder    ActionSnapshot `json:"get_order"`
	CancelOrder ActionSnapshot `json:"cancel_order"`
	SyncAccount ActionSnapshot `json:"sync_account"`
	PingLastMs  float64        `json:"ping_last_ms"`
	WS          WSSnapshot     `json:"ws"`
	// ErrorsByClass counts errors per class; index == ErrClass constant.
	ErrorsByClass [ErrClassCount]int64 `json:"errors_by_class"`
}

// WSSnapshot is the serializable form of WSMetrics.
type WSSnapshot struct {
	StreamStarts    int64   `json:"stream_starts"`
	StreamErrors    int64   `json:"stream_errors"`
	LifecycleEvents int64   `json:"lifecycle_events"`
	FillEvents      int64   `json:"fill_events"`
	BalanceEvents   int64   `json:"balance_events"`
	IdleSec         float64 `json:"idle_sec"` // seconds since last WS event (0 = never received)
}

// ActionSnapshot is a serializable point-in-time copy of ActionMetrics.
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
	ws := &m.Metrics.WS
	idle := ws.IdleDuration()
	var idleSec float64
	if idle > 0 {
		idleSec = idle.Seconds()
	}
	var errByClass [ErrClassCount]int64
	for i := range errByClass {
		errByClass[i] = m.Metrics.ErrorsByClass[i].Load()
	}
	return MetricsSnapshot{
		Name:        m.Name(),
		PlaceOrder:  snapshotAction(&m.Metrics.PlaceOrder),
		GetOrder:    snapshotAction(&m.Metrics.GetOrder),
		CancelOrder: snapshotAction(&m.Metrics.CancelOrder),
		SyncAccount: snapshotAction(&m.Metrics.SyncAccount),
		PingLastMs:  float64(m.Metrics.PingLastNanos.Load()) / 1e6,
		WS: WSSnapshot{
			StreamStarts:    ws.StreamStarts.Load(),
			StreamErrors:    ws.StreamErrors.Load(),
			LifecycleEvents: ws.LifecycleEvents.Load(),
			FillEvents:      ws.FillEvents.Load(),
			BalanceEvents:   ws.BalanceEvents.Load(),
			IdleSec:         idleSec,
		},
		ErrorsByClass: errByClass,
	}
}

// ── Optional interface forwarding ─────────────────────────────────────────────
// MeteredExchange forwards all optional interfaces so type assertions on the
// wrapped exchange reach the underlying adapter.

// SyncAccount implements AccountSyncer — records latency and delegates to inner.
func (m *MeteredExchange) SyncAccount(ctx context.Context, creds Credentials, since *time.Time) (*AccountSnapshot, error) {
	s, ok := m.inner.(AccountSyncer)
	if !ok {
		return nil, fmt.Errorf("exchange %q does not implement AccountSyncer", m.Name())
	}
	start := time.Now()
	snap, err := s.SyncAccount(ctx, creds, since)
	m.Metrics.SyncAccount.record(time.Since(start), err)
	return snap, err
}

// StreamOrders implements AccountStreamer — wraps handlers to update WS metrics.
func (m *MeteredExchange) StreamOrders(
	ctx context.Context,
	creds Credentials,
	onLifecycle func(OrderLifecycleEvent),
	onFill func(WsFillEvent),
	onBalance func(BalanceEvent),
	onPosition func(PositionEvent),
	onRisk func(RiskEvent),
	onCredentialError func(string),
) error {
	s, ok := m.inner.(AccountStreamer)
	if !ok {
		return fmt.Errorf("exchange %q does not implement AccountStreamer", m.Name())
	}
	m.Metrics.WS.StreamStarts.Add(1)

	var wrappedLifecycle func(OrderLifecycleEvent)
	if onLifecycle != nil {
		wrappedLifecycle = func(ev OrderLifecycleEvent) {
			m.Metrics.WS.LifecycleEvents.Add(1)
			m.Metrics.WS.touchEvent()
			onLifecycle(ev)
		}
	}
	var wrappedFill func(WsFillEvent)
	if onFill != nil {
		wrappedFill = func(ev WsFillEvent) {
			m.Metrics.WS.FillEvents.Add(1)
			m.Metrics.WS.touchEvent()
			onFill(ev)
		}
	}
	var wrappedBalance func(BalanceEvent)
	if onBalance != nil {
		wrappedBalance = func(ev BalanceEvent) {
			m.Metrics.WS.BalanceEvents.Add(1)
			m.Metrics.WS.touchEvent()
			onBalance(ev)
		}
	}

	err := s.StreamOrders(ctx, creds, wrappedLifecycle, wrappedFill, wrappedBalance, onPosition, onRisk, onCredentialError)
	if err != nil {
		m.Metrics.WS.StreamErrors.Add(1)
	}
	return err
}

func (m *MeteredExchange) GetCurrentPrice(ctx context.Context, creds Credentials, symbol string) (decimal.Decimal, error) {
	if s, ok := m.inner.(PriceFetcher); ok {
		return s.GetCurrentPrice(ctx, creds, symbol)
	}
	return decimal.Zero, fmt.Errorf("exchange %q does not implement PriceFetcher", m.Name())
}

func (m *MeteredExchange) GetPendingOrders(ctx context.Context, creds Credentials, symbol string) ([]OrderResult, error) {
	if s, ok := m.inner.(OrderReconciler); ok {
		return s.GetPendingOrders(ctx, creds, symbol)
	}
	return nil, fmt.Errorf("exchange %q does not implement OrderReconciler", m.Name())
}

func (m *MeteredExchange) FilledOrders(ctx context.Context, creds Credentials, symbols []string, from, to time.Time) ([]AccountTransaction, error) {
	if s, ok := m.inner.(HistoryFetcher); ok {
		return s.FilledOrders(ctx, creds, symbols, from, to)
	}
	return nil, fmt.Errorf("exchange %q does not implement HistoryFetcher", m.Name())
}

func (m *MeteredExchange) PlaceExitOrders(ctx context.Context, creds Credentials, req ExitOrderRequest) (*ExitOrderResult, error) {
	if s, ok := m.inner.(ExitOrderPlacer); ok {
		return s.PlaceExitOrders(ctx, creds, req)
	}
	return nil, fmt.Errorf("exchange %q does not implement ExitOrderPlacer", m.Name())
}

func (m *MeteredExchange) SetLeverage(ctx context.Context, creds Credentials, symbol string, leverage int, marginType string) error {
	if s, ok := m.inner.(LeverageSetter); ok {
		return s.SetLeverage(ctx, creds, symbol, leverage, marginType)
	}
	return fmt.Errorf("exchange %q does not implement LeverageSetter", m.Name())
}

// GetSymbolFilters implements SymbolInfoProvider — public endpoint, no credentials.
// Delegates to inner if it implements SymbolInfoProvider.
func (m *MeteredExchange) GetSymbolFilters(ctx context.Context, symbol string) (SymbolFilters, error) {
	if s, ok := m.inner.(SymbolInfoProvider); ok {
		return s.GetSymbolFilters(ctx, symbol)
	}
	return SymbolFilters{}, fmt.Errorf("exchange %q does not implement SymbolInfoProvider", m.Name())
}

// GetFreeBalance implements SpotBalanceFetcher — delegates to inner.
// Used as a fallback when a SELL exit fails with insufficient balance.
func (m *MeteredExchange) GetFreeBalance(ctx context.Context, creds Credentials, asset string) (decimal.Decimal, error) {
	if s, ok := m.inner.(SpotBalanceFetcher); ok {
		return s.GetFreeBalance(ctx, creds, asset)
	}
	return decimal.Zero, fmt.Errorf("exchange %q does not implement SpotBalanceFetcher", m.Name())
}

// SupportsFutures delegates to inner if inner implements the method.
// Returns false for spot-only exchanges (safe default).
func (m *MeteredExchange) SupportsFutures() bool {
	if ft, ok := m.inner.(interface{ SupportsFutures() bool }); ok {
		return ft.SupportsFutures()
	}
	return false
}

// SupportsSpot delegates to inner if inner implements SpotSupportChecker.
// Returns true (safe default) when inner doesn't implement it — every adapter except
// fbinance supports spot and never needed to declare it.
func (m *MeteredExchange) SupportsSpot() bool {
	if st, ok := m.inner.(SpotSupportChecker); ok {
		return st.SupportsSpot()
	}
	return true
}

// SupportsIsolatedMargin implements IsolatedMarginTrader — delegates to inner.
// Returns false when inner does not implement IsolatedMarginTrader (cross margin only).
func (m *MeteredExchange) SupportsIsolatedMargin() bool {
	if ft, ok := m.inner.(IsolatedMarginTrader); ok {
		return ft.SupportsIsolatedMargin()
	}
	return false
}

// ClidSurfaces implements ClidCapable — delegates to inner.
// Callable on any receiver; falls back to zero ClidSurfaces when inner doesn't implement it.
func (m *MeteredExchange) ClidSurfaces() ClidSurfaces {
	if c, ok := m.inner.(ClidCapable); ok {
		return c.ClidSurfaces()
	}
	return ClidSurfaces{}
}

// GetOrderByClientOrderID implements ClientOrderQuerier — delegates to inner.
// Used by recoverAmbiguousPlace to confirm whether a timed-out order landed.
func (m *MeteredExchange) GetOrderByClientOrderID(ctx context.Context, creds Credentials, symbol string, market MarketKind, clientOrderID string) (*OrderResult, error) {
	if s, ok := m.inner.(ClientOrderQuerier); ok {
		return s.GetOrderByClientOrderID(ctx, creds, symbol, market, clientOrderID)
	}
	return nil, fmt.Errorf("exchange %q does not implement ClientOrderQuerier", m.Name())
}

// SyncTime implements TimeSyncer — delegates to inner.
// Syncs local clock against exchange server time to prevent -1021 errors.
func (m *MeteredExchange) SyncTime(ctx context.Context) error {
	if ts, ok := m.inner.(TimeSyncer); ok {
		return ts.SyncTime(ctx)
	}
	return nil // not an error; exchange simply doesn't support time sync
}

// ensure MeteredExchange satisfies Exchange and all optional interfaces at compile time.
var _ Exchange = (*MeteredExchange)(nil)
var _ AccountSyncer = (*MeteredExchange)(nil)
var _ AccountStreamer = (*MeteredExchange)(nil)
var _ FillStreamer = (*MeteredExchange)(nil)
var _ PriceFetcher = (*MeteredExchange)(nil)
var _ OrderReconciler = (*MeteredExchange)(nil)
var _ HistoryFetcher = (*MeteredExchange)(nil)
var _ ExitOrderPlacer = (*MeteredExchange)(nil)
var _ LeverageSetter = (*MeteredExchange)(nil)
var _ SymbolInfoProvider = (*MeteredExchange)(nil)
var _ SpotBalanceFetcher = (*MeteredExchange)(nil)
var _ ClidCapable = (*MeteredExchange)(nil)
var _ ClientOrderQuerier = (*MeteredExchange)(nil)
var _ TimeSyncer = (*MeteredExchange)(nil)
var _ IsolatedMarginTrader = (*MeteredExchange)(nil)
var _ SpotSupportChecker = (*MeteredExchange)(nil)

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
