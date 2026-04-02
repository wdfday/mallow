package stream

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"stream-data/internal/model"
)

// BarSink consumes completed bars from a BarAggregator.
type BarSink interface {
	RunBars(ctx context.Context, bars <-chan model.Bar)
}

// barState tracks the in-progress OHLCV state for one symbol.
type barState struct {
	openTime int64
	open     float64
	high     float64
	low      float64
	close    float64
	volume   float64
	hasData  bool
}

// BarAggregator is a TickSink that accumulates ticks into fixed-interval OHLCV bars
// and emits completed bars to a downstream BarSink (e.g. JetStreamPublisher).
//
// Bar boundaries are aligned to wall-clock multiples of interval:
//
//	barOpenTime = (tick.Timestamp / intervalMs) * intervalMs
//
// A bar is emitted when the first tick of the next interval arrives.
// Incomplete bars are flushed on graceful shutdown.
type BarAggregator struct {
	interval   time.Duration
	bars       map[string]*barState
	mu         sync.Mutex
	out        chan model.Bar
	downstream BarSink
}

// NewBarAggregator creates a BarAggregator that emits to downstream.
// Call Run to start consuming ticks.
func NewBarAggregator(interval time.Duration, downstream BarSink) *BarAggregator {
	return &BarAggregator{
		interval:   interval,
		bars:       make(map[string]*barState),
		out:        make(chan model.Bar, 256),
		downstream: downstream,
	}
}

// Run implements TickSink. It starts the downstream BarSink in a goroutine,
// then consumes ticks until ch is closed or ctx is cancelled.
func (a *BarAggregator) Run(ctx context.Context, ch <-chan model.Tick) {
	// start downstream consumer
	downDone := make(chan struct{})
	go func() {
		defer close(downDone)
		a.downstream.RunBars(ctx, a.out)
	}()

	defer func() {
		// flush all incomplete bars before closing output
		a.mu.Lock()
		flushed := 0
		for sym, s := range a.bars {
			if s.hasData {
				a.out <- model.Bar{
					OpenTime: s.openTime,
					Symbol:   sym,
					Open:     s.open,
					High:     s.high,
					Low:      s.low,
					Close:    s.close,
					Volume:   s.volume,
				}
				flushed++
			}
		}
		a.mu.Unlock()
		close(a.out)
		<-downDone
		slog.Info("bar aggregator stopped", "flushed_incomplete", flushed)
	}()

	intervalMs := a.interval.Milliseconds()
	var emitted int64

	for {
		select {
		case tick, ok := <-ch:
			if !ok {
				return
			}
			if bar := a.process(tick, intervalMs); bar != nil {
				a.out <- *bar
				emitted++
				if emitted%100 == 0 {
					slog.Debug("bar aggregator progress", "emitted", emitted)
				}
			}
		case <-ctx.Done():
			// drain remaining ticks before flushing incomplete bars
			for {
				select {
				case tick, ok := <-ch:
					if !ok {
						return
					}
					if bar := a.process(tick, intervalMs); bar != nil {
						a.out <- *bar
						emitted++
					}
				default:
					return
				}
			}
		}
	}
}

// process handles one tick. Returns a completed Bar if the tick belongs to
// a new interval (completing the previous one), or nil otherwise.
func (a *BarAggregator) process(tick model.Tick, intervalMs int64) *model.Bar {
	bucket := (tick.Timestamp / intervalMs) * intervalMs

	a.mu.Lock()
	defer a.mu.Unlock()

	s, exists := a.bars[tick.Symbol]

	var completed *model.Bar

	if exists && s.hasData && bucket > s.openTime {
		// tick belongs to a new interval — emit the completed bar
		completed = &model.Bar{
			OpenTime: s.openTime,
			Symbol:   tick.Symbol,
			Open:     s.open,
			High:     s.high,
			Low:      s.low,
			Close:    s.close,
			Volume:   s.volume,
		}
		// reset state for new interval
		s.openTime = bucket
		s.open = tick.Price
		s.high = tick.Price
		s.low = tick.Price
		s.close = tick.Price
		s.volume = tick.Volume
		s.hasData = true
		slog.Info("bar emitted",
			"symbol", completed.Symbol,
			"open_time", completed.OpenTime,
			"o", completed.Open,
			"h", completed.High,
			"l", completed.Low,
			"c", completed.Close,
			"v", completed.Volume,
		)
		return completed
	}

	if !exists || !s.hasData {
		// first tick for this symbol (or after flush)
		a.bars[tick.Symbol] = &barState{
			openTime: bucket,
			open:     tick.Price,
			high:     tick.Price,
			low:      tick.Price,
			close:    tick.Price,
			volume:   tick.Volume,
			hasData:  true,
		}
		return nil
	}

	// same interval — update OHLCV
	if tick.Price > s.high {
		s.high = tick.Price
	}
	if tick.Price < s.low {
		s.low = tick.Price
	}
	s.close = tick.Price
	s.volume += tick.Volume

	return nil
}
