package okx

import (
	"testing"

	"stream-data/internal/model"
)

func TestHandleCandle_Closed(t *testing.T) {
	p := New()
	out := make(chan model.Bar, 10)

	// confirm = "1" → bar is closed, should emit
	msg := []byte(`{
		"arg": {"channel": "candle1m", "instId": "BTC-USDT"},
		"data": [["1705312200000","42000.5","42100.0","41900.0","42050.0","10.5","441525.0","441525.0","1"]]
	}`)

	p.handleCandle(msg, out)

	if len(out) != 1 {
		t.Fatalf("expected 1 bar, got %d", len(out))
	}
	bar := <-out
	if bar.Symbol != "BTC-USDT" {
		t.Errorf("symbol = %q, want BTC-USDT", bar.Symbol)
	}
	if bar.Open != 42000.5 {
		t.Errorf("open = %f, want 42000.5", bar.Open)
	}
	if bar.High != 42100.0 {
		t.Errorf("high = %f, want 42100.0", bar.High)
	}
	if bar.Low != 41900.0 {
		t.Errorf("low = %f, want 41900.0", bar.Low)
	}
	if bar.Close != 42050.0 {
		t.Errorf("close = %f, want 42050.0", bar.Close)
	}
	if bar.OpenTime != 1705312200000 {
		t.Errorf("open_time = %d, want 1705312200000", bar.OpenTime)
	}
}

func TestHandleCandle_InProgress(t *testing.T) {
	p := New()
	out := make(chan model.Bar, 10)

	// confirm = "0" → bar still in progress, should NOT emit
	msg := []byte(`{
		"arg": {"channel": "candle1m", "instId": "BTC-USDT"},
		"data": [["1705312200000","42000.5","42100.0","41900.0","42050.0","10.5","441525.0","441525.0","0"]]
	}`)

	p.handleCandle(msg, out)

	if len(out) != 0 {
		t.Errorf("in-progress candle should not emit, got %d bars", len(out))
	}
}

func TestHandleCandle_Pong(t *testing.T) {
	p := New()
	out := make(chan model.Bar, 10)

	p.handleCandle([]byte("pong"), out)

	if len(out) != 0 {
		t.Errorf("pong should not emit bars, got %d", len(out))
	}
}

func TestHandleCandle_SubscriptionConfirm(t *testing.T) {
	p := New()
	out := make(chan model.Bar, 10)

	msg := []byte(`{"event": "subscribe", "arg": {"channel": "candle1m", "instId": "BTC-USDT"}}`)
	p.handleCandle(msg, out)

	if len(out) != 0 {
		t.Errorf("subscription confirm should not emit bars, got %d", len(out))
	}
}
