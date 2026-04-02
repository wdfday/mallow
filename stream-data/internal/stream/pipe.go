package stream

import (
	"context"
	"sync"

	"stream-data/internal/model"
)

// Merge fans N tick channels into one. The output channel is closed when all
// input channels are drained. Callers cancel the context to stop providers.
func Merge(channels ...<-chan model.Tick) <-chan model.Tick {
	out := make(chan model.Tick, 512)
	var wg sync.WaitGroup
	wg.Add(len(channels))
	for _, ch := range channels {
		ch := ch
		go func() {
			defer wg.Done()
			for t := range ch {
				out <- t
			}
		}()
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// MergeBars fans N bar channels into one.
func MergeBars(channels ...<-chan model.Bar) <-chan model.Bar {
	out := make(chan model.Bar, 256)
	var wg sync.WaitGroup
	wg.Add(len(channels))
	for _, ch := range channels {
		go func(c <-chan model.Bar) {
			defer wg.Done()
			for b := range c {
				out <- b
			}
		}(ch)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// FanOut broadcasts ticks from one source channel to multiple TickSinks.
// Each sink receives its own copy via a dedicated goroutine.
type FanOut struct {
	sinks []TickSink
}

// NewFanOut creates a multiplexer that broadcasts to all provided sinks.
func NewFanOut(sinks ...TickSink) *FanOut {
	return &FanOut{sinks: sinks}
}

// Run distributes every tick to all sinks via fan-out channels.
// Blocks until ctx is cancelled and all sinks finish.
func (f *FanOut) Run(ctx context.Context, ch <-chan model.Tick) {
	if len(f.sinks) == 0 {
		return
	}

	outs := make([]chan model.Tick, len(f.sinks))
	for i := range outs {
		outs[i] = make(chan model.Tick, 256)
	}

	var wg sync.WaitGroup
	wg.Add(len(f.sinks))
	for i, sink := range f.sinks {
		i, sink := i, sink
		go func() {
			defer wg.Done()
			sink.Run(ctx, outs[i])
		}()
	}

	for tick := range ch {
		for _, out := range outs {
			out <- tick
		}
	}

	for _, out := range outs {
		close(out)
	}

	wg.Wait()
}
