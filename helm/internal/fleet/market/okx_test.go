package market

import (
	"testing"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

func TestOKXHandleMessage_TradeData(t *testing.T) {
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
		"arg": {"channel": "trades", "instId": "BTC-USDT"},
		"data": [
			{"instId": "BTC-USDT", "tradeId": "123", "px": "42000.5", "sz": "0.001", "side": "buy", "ts": "1705312200123"},
			{"instId": "ETH-USDT", "tradeId": "124", "px": "2200.0", "sz": "0.5", "side": "sell", "ts": "1705312200456"}
		]
	}`)

	okxHandleMessage(msg, onTick, onBook)

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

func TestOKXHandleMessage_Pong(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) { called = true }
	onBook := func(string, exchange.L2Snapshot) {}

	okxHandleMessage([]byte("pong"), onTick, onBook)

	if called {
		t.Error("pong should not trigger onTick")
	}
}

func TestOKXHandleMessage_SubscriptionConfirm(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) { called = true }
	onBook := func(string, exchange.L2Snapshot) {}

	msg := []byte(`{"event": "subscribe", "arg": {"channel": "trades", "instId": "BTC-USDT"}}`)
	okxHandleMessage(msg, onTick, onBook)

	if called {
		t.Error("subscription confirm should not trigger onTick")
	}
}

func TestOKXHandleMessage_InvalidPrice(t *testing.T) {
	var calls int
	onTick := func(string, decimal.Decimal) { calls++ }
	onBook := func(string, exchange.L2Snapshot) {}

	msg := []byte(`{
		"arg": {"channel": "trades", "instId": "BTC-USDT"},
		"data": [
			{"instId": "BTC-USDT", "tradeId": "1", "px": "0", "sz": "1", "side": "buy", "ts": "123"},
			{"instId": "BTC-USDT", "tradeId": "2", "px": "invalid", "sz": "1", "side": "buy", "ts": "456"}
		]
	}`)

	okxHandleMessage(msg, onTick, onBook)

	if calls != 0 {
		t.Errorf("expected 0 calls for invalid/zero prices, got %d", calls)
	}
}

func TestOKXHandleMessage_Books5(t *testing.T) {
	var got exchange.L2Snapshot
	calls := 0
	onTick := func(string, decimal.Decimal) {}
	onBook := func(symbol string, snap exchange.L2Snapshot) {
		got = snap
		calls++
	}

	msg := []byte(`{
		"arg": {"channel": "books5", "instId": "BTC-USDT"},
		"data": [{
			"asks": [["42001.0","0.8","0","2"],["42002.0","1.2","0","1"]],
			"bids": [["42000.0","1.5","0","3"],["41999.5","2.0","0","1"]],
			"instId": "BTC-USDT",
			"ts": "1670324386802",
			"seqId": 123456
		}]
	}`)

	okxHandleMessage(msg, onTick, onBook)

	if calls != 1 {
		t.Fatalf("expected 1 onBook call, got %d", calls)
	}
	if got.Symbol != "BTC-USDT" {
		t.Errorf("symbol = %q, want BTC-USDT", got.Symbol)
	}
	wantBid0, _ := decimal.NewFromString("42000.0")
	if !got.Bids[0].Price.Equal(wantBid0) {
		t.Errorf("bid[0].Price = %s, want 42000.0", got.Bids[0].Price)
	}
	wantAsk0, _ := decimal.NewFromString("42001.0")
	if !got.Asks[0].Price.Equal(wantAsk0) {
		t.Errorf("ask[0].Price = %s, want 42001.0", got.Asks[0].Price)
	}
	if got.Timestamp.UnixMilli() != 1670324386802 {
		t.Errorf("timestamp = %d, want 1670324386802", got.Timestamp.UnixMilli())
	}
}
