package market

import (
	"testing"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

func TestBinanceHandleMessage_Trade(t *testing.T) {
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
	onBook := func(string, exchange.L2Snapshot) {}

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

	binanceHandleMessage(msg, onTick, onBook)

	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].symbol != "BTCUSDT" {
		t.Errorf("symbol = %q, want BTCUSDT", calls[0].symbol)
	}
	want, _ := decimal.NewFromString("42000.5")
	if !calls[0].price.Equal(want) {
		t.Errorf("price = %s, want 42000.5", calls[0].price)
	}
}

func TestBinanceHandleMessage_NonTrade(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) { called = true }
	onBook := func(string, exchange.L2Snapshot) {}

	msg := []byte(`{
		"stream": "btcusdt@kline_1m",
		"data": {
			"e": "kline",
			"s": "BTCUSDT"
		}
	}`)

	binanceHandleMessage(msg, onTick, onBook)

	if called {
		t.Error("non-trade event should not trigger onTick")
	}
}

func TestBinanceHandleMessage_InvalidJSON(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) { called = true }
	onBook := func(string, exchange.L2Snapshot) {}

	binanceHandleMessage([]byte("invalid json"), onTick, onBook)

	if called {
		t.Error("invalid JSON should not trigger onTick")
	}
}

func TestBinanceHandleMessage_Depth5(t *testing.T) {
	var got exchange.L2Snapshot
	calls := 0
	onTick := func(string, decimal.Decimal) {}
	onBook := func(symbol string, snap exchange.L2Snapshot) {
		got = snap
		calls++
	}

	msg := []byte(`{
		"stream": "btcusdt@depth5@100ms",
		"data": {
			"lastUpdateId": 123456,
			"bids": [["42000.0","1.5"],["41999.5","2.0"]],
			"asks": [["42001.0","0.8"],["42002.0","1.2"]]
		}
	}`)

	binanceHandleMessage(msg, onTick, onBook)

	if calls != 1 {
		t.Fatalf("expected 1 onBook call, got %d", calls)
	}
	if got.Symbol != "BTCUSDT" {
		t.Errorf("symbol = %q, want BTCUSDT", got.Symbol)
	}
	wantBid0, _ := decimal.NewFromString("42000.0")
	if !got.Bids[0].Price.Equal(wantBid0) {
		t.Errorf("bid[0].Price = %s, want 42000.0", got.Bids[0].Price)
	}
	wantAsk0, _ := decimal.NewFromString("42001.0")
	if !got.Asks[0].Price.Equal(wantAsk0) {
		t.Errorf("ask[0].Price = %s, want 42001.0", got.Asks[0].Price)
	}
	// Only 2 levels sent — the rest of the fixed [5]L2Level array stays zero.
	if !got.Bids[2].Price.IsZero() {
		t.Errorf("bid[2] should be zero-value, got %+v", got.Bids[2])
	}
}

func TestBinanceHandleMessage_Depth5_NoSymbolMatch(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) {}
	onBook := func(string, exchange.L2Snapshot) { called = true }

	msg := []byte(`{"stream": "btcusdt@depth5@100ms", "data": {"lastUpdateId":1,"bids":[],"asks":[]}}`)
	binanceHandleMessage(msg, onTick, onBook)

	if called {
		t.Error("empty bids/asks should not trigger onBook")
	}
}
