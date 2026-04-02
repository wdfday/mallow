package binance

import (
	"testing"
)

func TestHandleMessage_Trade(t *testing.T) {
	var calls []struct {
		symbol string
		price  float64
	}
	onTick := func(symbol string, price float64) {
		calls = append(calls, struct {
			symbol string
			price  float64
		}{symbol, price})
	}

	msg := []byte(`{
		"stream": "btcusdt@trade",
		"data": {
			"e": "trade",
			"s": "BTCUSDT",
			"p": "42000.5",
			"q": "0.001",
			"T": 1705312200123,
			"m": true
		}
	}`)

	handleMessage(msg, onTick)

	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].symbol != "BTCUSDT" {
		t.Errorf("symbol = %q, want BTCUSDT", calls[0].symbol)
	}
	if calls[0].price != 42000.5 {
		t.Errorf("price = %f, want 42000.5", calls[0].price)
	}
}

func TestHandleMessage_NonTrade(t *testing.T) {
	called := false
	onTick := func(string, float64) { called = true }

	msg := []byte(`{
		"stream": "btcusdt@kline_1m",
		"data": {
			"e": "kline",
			"s": "BTCUSDT"
		}
	}`)

	handleMessage(msg, onTick)

	if called {
		t.Error("non-trade event should not trigger onTick")
	}
}

func TestHandleMessage_InvalidJSON(t *testing.T) {
	called := false
	onTick := func(string, float64) { called = true }

	handleMessage([]byte("invalid json"), onTick)

	if called {
		t.Error("invalid JSON should not trigger onTick")
	}
}
