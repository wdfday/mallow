package bybit

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestHandleMessage_Trade(t *testing.T) {
	var calls []struct {
		symbol string
		price  decimal.Decimal
	}
	onTick := func(symbol string, price decimal.Decimal) {
		calls = append(calls, struct {
			symbol string
			price  decimal.Decimal
		}{symbol, price})
	}

	msg := []byte(`{
		"topic": "publicTrade.BTCUSDT",
		"data": [
			{"S": "BTCUSDT", "p": "42000.5", "v": "0.001", "T": 1705312200123},
			{"S": "BTCUSDT", "p": "42001.0", "v": "0.002", "T": 1705312200456}
		]
	}`)

	handleMessage(msg, onTick)

	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	want0, _ := decimal.NewFromString("42000.5")
	want1, _ := decimal.NewFromString("42001.0")
	if calls[0].symbol != "BTCUSDT" || !calls[0].price.Equal(want0) {
		t.Errorf("call[0] = %+v, want BTCUSDT/42000.5", calls[0])
	}
	if !calls[1].price.Equal(want1) {
		t.Errorf("call[1] price = %s, want 42001.0", calls[1].price)
	}
}

func TestHandleMessage_EmptyTopic(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) { called = true }

	msg := []byte(`{"topic": "", "data": []}`)
	handleMessage(msg, onTick)

	if called {
		t.Error("empty topic should not trigger onTick")
	}
}

func TestHandleMessage_InvalidPrice(t *testing.T) {
	var calls int
	onTick := func(string, decimal.Decimal) { calls++ }

	msg := []byte(`{
		"topic": "publicTrade.BTCUSDT",
		"data": [
			{"S": "BTCUSDT", "p": "invalid", "v": "1", "T": 123}
		]
	}`)

	handleMessage(msg, onTick)

	if calls != 0 {
		t.Errorf("expected 0 calls for invalid price, got %d", calls)
	}
}
