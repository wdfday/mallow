package okx

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestHandleMessage_TradeData(t *testing.T) {
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
		"arg": {"channel": "trades", "instId": "BTC-USDT"},
		"data": [
			{"instId": "BTC-USDT", "tradeId": "123", "px": "42000.5", "sz": "0.001", "side": "buy", "ts": "1705312200123"},
			{"instId": "ETH-USDT", "tradeId": "124", "px": "2200.0", "sz": "0.5", "side": "sell", "ts": "1705312200456"}
		]
	}`)

	handleMessage(msg, onTick)

	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}

	if calls[0].symbol != "BTC-USDT" {
		t.Errorf("call[0] symbol = %q, want BTC-USDT", calls[0].symbol)
	}
	want0, _ := decimal.NewFromString("42000.5")
	if !calls[0].price.Equal(want0) {
		t.Errorf("call[0] price = %s, want 42000.5", calls[0].price)
	}
	if calls[1].symbol != "ETH-USDT" {
		t.Errorf("call[1] symbol = %q, want ETH-USDT", calls[1].symbol)
	}
	want1, _ := decimal.NewFromString("2200.0")
	if !calls[1].price.Equal(want1) {
		t.Errorf("call[1] price = %s, want 2200.0", calls[1].price)
	}
}

func TestHandleMessage_Pong(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) { called = true }

	handleMessage([]byte("pong"), onTick)

	if called {
		t.Error("pong should not trigger onTick")
	}
}

func TestHandleMessage_SubscriptionConfirm(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) { called = true }

	msg := []byte(`{"event": "subscribe", "arg": {"channel": "trades", "instId": "BTC-USDT"}}`)
	handleMessage(msg, onTick)

	if called {
		t.Error("subscription confirm should not trigger onTick")
	}
}

func TestHandleMessage_InvalidPrice(t *testing.T) {
	var calls int
	onTick := func(string, decimal.Decimal) { calls++ }

	msg := []byte(`{
		"arg": {"channel": "trades", "instId": "BTC-USDT"},
		"data": [
			{"instId": "BTC-USDT", "tradeId": "1", "px": "0", "sz": "1", "side": "buy", "ts": "123"},
			{"instId": "BTC-USDT", "tradeId": "2", "px": "invalid", "sz": "1", "side": "buy", "ts": "456"}
		]
	}`)

	handleMessage(msg, onTick)

	if calls != 0 {
		t.Errorf("expected 0 calls for invalid/zero prices, got %d", calls)
	}
}
