package stream

import (
	"context"
	"testing"
	"time"

	"stream-data/internal/model"
)

// mockBarSink collects bars for test assertion.
type mockBarSink struct {
	bars []model.Bar
}

func (m *mockBarSink) RunBars(_ context.Context, ch <-chan model.Bar) {
	for bar := range ch {
		m.bars = append(m.bars, bar)
	}
}

// makeTick builds a Tick with given symbol, price, volume, and Unix-ms timestamp.
func makeTick(symbol string, price, volume float64, tsMs int64) model.Tick {
	return model.Tick{
		Symbol:    symbol,
		Price:     price,
		Volume:    volume,
		Timestamp: tsMs,
	}
}

func TestBarAggregator_OneCompleteBar(t *testing.T) {
	sink := &mockBarSink{}
	agg := NewBarAggregator(time.Minute, sink)

	ch := make(chan model.Tick, 10)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		agg.Run(ctx, ch)
	}()

	// Minute 0: three ticks
	min0 := int64(0)
	ch <- makeTick("BTCUSDT", 100.0, 1.0, min0+1000)
	ch <- makeTick("BTCUSDT", 105.0, 2.0, min0+2000)
	ch <- makeTick("BTCUSDT", 98.0, 1.5, min0+3000)

	// Minute 1: first tick triggers emit of minute-0 bar
	min1 := int64(60_000)
	ch <- makeTick("BTCUSDT", 110.0, 0.5, min1+1000)

	// Shutdown to flush minute-1 incomplete bar
	cancel()
	close(ch)
	<-done

	if len(sink.bars) < 1 {
		t.Fatalf("expected at least 1 bar, got %d", len(sink.bars))
	}

	bar := sink.bars[0]
	if bar.Symbol != "BTCUSDT" {
		t.Errorf("symbol: got %q want %q", bar.Symbol, "BTCUSDT")
	}
	if bar.OpenTime != min0 {
		t.Errorf("open_time: got %d want %d", bar.OpenTime, min0)
	}
	if bar.Open != 100.0 {
		t.Errorf("open: got %v want 100.0", bar.Open)
	}
	if bar.High != 105.0 {
		t.Errorf("high: got %v want 105.0", bar.High)
	}
	if bar.Low != 98.0 {
		t.Errorf("low: got %v want 98.0", bar.Low)
	}
	if bar.Close != 98.0 {
		t.Errorf("close: got %v want 98.0", bar.Close)
	}
	if bar.Volume != 4.5 {
		t.Errorf("volume: got %v want 4.5", bar.Volume)
	}
}

func TestBarAggregator_FlushIncompleteOnShutdown(t *testing.T) {
	sink := &mockBarSink{}
	agg := NewBarAggregator(time.Minute, sink)

	ch := make(chan model.Tick, 10)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		agg.Run(ctx, ch)
	}()

	min0 := int64(0)
	ch <- makeTick("ETHUSDT", 200.0, 3.0, min0+5000)
	ch <- makeTick("ETHUSDT", 210.0, 1.0, min0+10000)

	// Shutdown without crossing interval boundary
	cancel()
	close(ch)
	<-done

	// Incomplete bar should be flushed
	if len(sink.bars) != 1 {
		t.Fatalf("expected 1 flushed bar, got %d", len(sink.bars))
	}
	bar := sink.bars[0]
	if bar.Open != 200.0 {
		t.Errorf("open: got %v want 200.0", bar.Open)
	}
	if bar.Close != 210.0 {
		t.Errorf("close: got %v want 210.0", bar.Close)
	}
	if bar.Volume != 4.0 {
		t.Errorf("volume: got %v want 4.0", bar.Volume)
	}
}

func TestBarAggregator_MultiSymbol(t *testing.T) {
	sink := &mockBarSink{}
	agg := NewBarAggregator(time.Minute, sink)

	ch := make(chan model.Tick, 20)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		agg.Run(ctx, ch)
	}()

	min0 := int64(0)
	min1 := int64(60_000)

	// Two symbols interleaved
	ch <- makeTick("BTCUSDT", 100.0, 1.0, min0+1000)
	ch <- makeTick("ETHUSDT", 50.0, 5.0, min0+2000)
	ch <- makeTick("BTCUSDT", 102.0, 2.0, min0+3000)

	// Trigger emit for both by crossing minute boundary
	ch <- makeTick("BTCUSDT", 103.0, 0.5, min1+1000)
	ch <- makeTick("ETHUSDT", 51.0, 1.0, min1+2000)

	cancel()
	close(ch)
	<-done

	// Should have at least 2 complete bars (one per symbol from min0)
	if len(sink.bars) < 2 {
		t.Fatalf("expected at least 2 bars, got %d", len(sink.bars))
	}

	bySymbol := make(map[string]model.Bar)
	for _, b := range sink.bars {
		if b.OpenTime == min0 {
			bySymbol[b.Symbol] = b
		}
	}

	btc, ok := bySymbol["BTCUSDT"]
	if !ok {
		t.Fatal("no BTCUSDT bar from min0")
	}
	if btc.Open != 100.0 || btc.High != 102.0 || btc.Close != 102.0 {
		t.Errorf("BTCUSDT bar incorrect: %+v", btc)
	}

	eth, ok := bySymbol["ETHUSDT"]
	if !ok {
		t.Fatal("no ETHUSDT bar from min0")
	}
	if eth.Open != 50.0 || eth.Close != 50.0 {
		t.Errorf("ETHUSDT bar incorrect: %+v", eth)
	}
}

func TestBarAggregator_TwoBarsInSequence(t *testing.T) {
	sink := &mockBarSink{}
	agg := NewBarAggregator(time.Minute, sink)

	ch := make(chan model.Tick, 20)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		agg.Run(ctx, ch)
	}()

	min0, min1, min2 := int64(0), int64(60_000), int64(120_000)

	ch <- makeTick("BTCUSDT", 100.0, 1.0, min0+1000)
	ch <- makeTick("BTCUSDT", 101.0, 1.0, min1+1000) // emits min0 bar
	ch <- makeTick("BTCUSDT", 102.0, 1.0, min2+1000) // emits min1 bar

	cancel()
	close(ch)
	<-done

	// min0 and min1 complete, min2 flushed as incomplete → 3 bars total
	if len(sink.bars) != 3 {
		t.Fatalf("expected 3 bars, got %d: %+v", len(sink.bars), sink.bars)
	}

	if sink.bars[0].OpenTime != min0 {
		t.Errorf("bars[0].OpenTime: got %d want %d", sink.bars[0].OpenTime, min0)
	}
	if sink.bars[1].OpenTime != min1 {
		t.Errorf("bars[1].OpenTime: got %d want %d", sink.bars[1].OpenTime, min1)
	}
}
