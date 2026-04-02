package tick

import (
	"log/slog"
	"math"
	"orchestrator/internal/infra/engine"
	"sync"
	"time"
)

// BarBuilder aggregates raw ticks into OHLCV bars at a fixed interval (e.g. 5 min).
//
// For each symbol, it accumulates ticks and emits a completed BarMsg when the
// interval boundary is crossed. The emitted bar covers [windowStart, windowStart+interval).
//
// Thread-safe: multiple goroutines may call AddTick concurrently.
type BarBuilder struct {
	interval time.Duration
	mu       sync.Mutex
	windows  map[string]*barWindow // symbol → current accumulating window
	onBar    func(*engine.BarMsg)  // callback invoked when a bar completes
}

// barWindow tracks the in-progress OHLCV aggregation for one symbol.
type barWindow struct {
	symbol      string
	windowStart time.Time // start of the current interval
	open        float64
	high        float64
	low         float64
	close       float64
	volume      float64
	count       int64
}

// NewBarBuilder creates a BarBuilder that emits bars every `interval`.
// The onBar callback is invoked synchronously when a bar completes;
// callers should keep it fast or dispatch to a goroutine.
func NewBarBuilder(interval time.Duration, onBar func(*engine.BarMsg)) *BarBuilder {
	return &BarBuilder{
		interval: interval,
		windows:  make(map[string]*barWindow),
		onBar:    onBar,
	}
}

// Tick represents a decoded tick from NATS (matches stream-data model.Tick).
type Tick struct {
	Timestamp int64 // Unix ms
	Symbol    string
	Price     float64
	Volume    float64
}

// AddTick processes a single tick. If the tick's timestamp falls outside the
// current window for its symbol, the completed bar is emitted first.
func (b *BarBuilder) AddTick(t Tick) {
	tickTime := time.UnixMilli(t.Timestamp).UTC()
	windowStart := tickTime.Truncate(b.interval)

	b.mu.Lock()
	defer b.mu.Unlock()

	w, exists := b.windows[t.Symbol]
	if !exists {
		// First tick for this symbol — start a new window.
		b.windows[t.Symbol] = &barWindow{
			symbol:      t.Symbol,
			windowStart: windowStart,
			open:        t.Price,
			high:        t.Price,
			low:         t.Price,
			close:       t.Price,
			volume:      t.Volume,
			count:       1,
		}
		return
	}

	// If tick belongs to a new window, emit the completed bar and start fresh.
	if windowStart.After(w.windowStart) {
		b.emit(w)
		b.windows[t.Symbol] = &barWindow{
			symbol:      t.Symbol,
			windowStart: windowStart,
			open:        t.Price,
			high:        t.Price,
			low:         t.Price,
			close:       t.Price,
			volume:      t.Volume,
			count:       1,
		}
		return
	}

	// Same window — update OHLCV.
	w.high = math.Max(w.high, t.Price)
	w.low = math.Min(w.low, t.Price)
	w.close = t.Price
	w.volume += t.Volume
	w.count++
}

// FlushAll emits bars for all open windows (e.g. on shutdown).
func (b *BarBuilder) FlushAll() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for symbol, w := range b.windows {
		b.emit(w)
		delete(b.windows, symbol)
	}
}

// emit converts a completed window to a BarMsg and invokes the callback.
// Must be called with b.mu held.
func (b *BarBuilder) emit(w *barWindow) {
	bar := engine.BarMsg{
		T: w.windowStart.UnixMilli(),
		S: w.symbol,
		O: w.open,
		H: w.high,
		L: w.low,
		C: w.close,
		V: w.volume,
	}

	slog.Info("bar emitted",
		"symbol", bar.S,
		"open", bar.O,
		"high", bar.H,
		"low", bar.L,
		"close", bar.C,
		"volume", bar.V,
		"ticks", w.count,
		"window", w.windowStart.Format("15:04"),
	)

	b.onBar(&bar)
}

// ActiveSymbols returns the list of symbols currently being tracked.
func (b *BarBuilder) ActiveSymbols() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	symbols := make([]string, 0, len(b.windows))
	for s := range b.windows {
		symbols = append(symbols, s)
	}
	return symbols
}
