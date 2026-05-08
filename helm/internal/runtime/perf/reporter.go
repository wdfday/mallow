package perf

import "time"

// Reporter computes a HandReport snapshot for a running hand.
// Implementations are expected to be stateless calculators — they read
// from the hand's Portfolio and HandMetrics, compute, and return.
// All heavy computation (Sharpe, Sortino, equity-curve scan) happens here,
// not in the hot signal path.
type Reporter interface {
	// Report builds a full HandReport as of now.
	// handID, symbol, strategy are metadata carried through unchanged.
	// source provides the raw trading data to compute from.
	Report(handID, symbol, strategy string, source Source) HandReport
}

// Source is the read-only view of a hand's state that Reporter needs.
// Keeping this as an interface decouples the reporter from runtime internals
// and makes it straightforward to unit-test with stub data.
type Source interface {
	// Since returns the time the hand last started running.
	Since() time.Time

	// ReturnSeries returns the equity curve as a slice of (time, equity) points,
	// ordered oldest-first. Used to compute Sharpe, Sortino, drawdown series.
	ReturnSeries() []EquityPoint

	// ClosedTrades returns all completed round-trip trades, ordered by exit time.
	ClosedTrades() []ClosedTrade

	// FillEvents returns all fill records since the hand started, ordered by time.
	// Used to compute slippage, latency, and fill-rate metrics.
	FillEvents() []FillEvent

	// SignalCounts returns the current signal pipeline counters.
	SignalCounts() SignalStats

	// OpenPositionCount returns the number of currently open positions.
	OpenPositionCount() int

	// UnrealizedPnL returns the mark-to-market PnL of all open positions.
	UnrealizedPnL() float64

	// CurrentEquity returns current total equity (cash + mark-to-market positions).
	CurrentEquity() float64
}

// ── Input value types used by Source ─────────────────────────────────────────

// EquityPoint is a (time, equity) snapshot for the equity curve.
// Mirrors portfolio.EquityPoint but kept local to avoid import cycles.
type EquityPoint struct {
	Timestamp time.Time
	Equity    float64
}

// ClosedTrade is a completed round-trip with the fields Reporter needs.
type ClosedTrade struct {
	EntryTime time.Time
	ExitTime  time.Time
	PnL       float64 // absolute, in quote currency
	PnLPct    float64 // return on invested capital for this trade
}

// FillEvent carries execution quality data for one filled order.
type FillEvent struct {
	PlacedAt    time.Time
	FilledAt    time.Time
	SignalAt    time.Time // time the triggering signal was received
	ExpectedPx  float64   // mid/signal price at order time
	FilledPx    float64
	Commission  float64
	WasFull     bool // false = partial fill
	WasRejected bool
}
