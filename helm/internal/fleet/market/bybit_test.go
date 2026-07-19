package market

import (
	"testing"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// Note on the trade fixture field mapping: real Bybit v5 publicTrade payloads
// use lowercase "s" for the symbol and uppercase "S" for the side ("Buy"/"Sell") —
// see bybitTradeMsg. The previous helm/internal/infra/marketdata/bybit
// implementation had this backwards (`Symbol string \`json:"S"\“), which meant
// wsSymbol on a real feed would have decoded to "Buy"/"Sell" instead of the
// actual pair. Fixed here; these fixtures use the corrected (real) schema.
func TestBybitHandleMessage_Trade(t *testing.T) {
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
	books := map[string]*bybitBook{}

	msg := []byte(`{
		"topic": "publicTrade.BTCUSDT",
		"type": "snapshot",
		"data": [
			{"T": 1705312200123, "s": "BTCUSDT", "S": "Buy", "p": "42000.5", "v": "0.001"},
			{"T": 1705312200456, "s": "BTCUSDT", "S": "Sell", "p": "42001.0", "v": "0.002"}
		]
	}`)

	bybitHandleMessage(msg, books, onTick, onBook)

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

func TestBybitHandleMessage_InvalidPrice(t *testing.T) {
	var calls int
	onTick := func(string, decimal.Decimal) { calls++ }
	onBook := func(string, exchange.L2Snapshot) {}
	books := map[string]*bybitBook{}

	msg := []byte(`{
		"topic": "publicTrade.BTCUSDT",
		"type": "snapshot",
		"data": [
			{"T": 123, "s": "BTCUSDT", "S": "Buy", "p": "invalid", "v": "1"}
		]
	}`)

	bybitHandleMessage(msg, books, onTick, onBook)

	if calls != 0 {
		t.Errorf("expected 0 calls for invalid price, got %d", calls)
	}
}

func TestBybitHandleMessage_InvalidJSON(t *testing.T) {
	called := false
	onTick := func(string, decimal.Decimal) { called = true }
	onBook := func(string, exchange.L2Snapshot) { called = true }
	books := map[string]*bybitBook{}

	bybitHandleMessage([]byte("not json"), books, onTick, onBook)

	if called {
		t.Error("invalid JSON should not trigger any callback")
	}
}

// TestBybitHandleMessage_OrderbookSnapshotThenDelta exercises the real
// snapshot+delta local order-book merge: a snapshot seeds the book, a delta
// upserts one level and removes another (size "0"), and the top5 extraction
// must reflect the merged state, not just the latest message's raw levels.
func TestBybitHandleMessage_OrderbookSnapshotThenDelta(t *testing.T) {
	var got exchange.L2Snapshot
	onTick := func(string, decimal.Decimal) {}
	onBook := func(symbol string, snap exchange.L2Snapshot) { got = snap }
	books := map[string]*bybitBook{}

	snapshot := []byte(`{
		"topic": "orderbook.50.BTCUSDT",
		"type": "snapshot",
		"ts": 1,
		"data": {
			"s": "BTCUSDT",
			"b": [["42000.0","1.5"],["41999.0","2.0"]],
			"a": [["42001.0","0.8"],["42002.0","1.2"]],
			"u": 1,
			"seq": 1
		}
	}`)
	bybitHandleMessage(snapshot, books, onTick, onBook)

	wantBid0, _ := decimal.NewFromString("42000.0")
	if !got.Bids[0].Price.Equal(wantBid0) {
		t.Fatalf("after snapshot: bid[0].Price = %s, want 42000.0", got.Bids[0].Price)
	}

	// Delta: remove the best bid (42000.0 → size 0), upsert a new best ask (42000.5).
	delta := []byte(`{
		"topic": "orderbook.50.BTCUSDT",
		"type": "delta",
		"ts": 2,
		"data": {
			"s": "BTCUSDT",
			"b": [["42000.0","0"]],
			"a": [["42000.5","0.3"]],
			"u": 2,
			"seq": 2
		}
	}`)
	bybitHandleMessage(delta, books, onTick, onBook)

	// Best bid should now be the next level down (41999.0) — 42000.0 was removed.
	wantBidAfter, _ := decimal.NewFromString("41999.0")
	if !got.Bids[0].Price.Equal(wantBidAfter) {
		t.Errorf("after delta: bid[0].Price = %s, want 41999.0 (42000.0 should have been removed)", got.Bids[0].Price)
	}
	// Best ask should now be the newly upserted 42000.5, lower than the snapshot's 42001.0.
	wantAskAfter, _ := decimal.NewFromString("42000.5")
	if !got.Asks[0].Price.Equal(wantAskAfter) {
		t.Errorf("after delta: ask[0].Price = %s, want 42000.5", got.Asks[0].Price)
	}
}

func TestBybitBook_Top5_SortOrder(t *testing.T) {
	b := newBybitBook()
	b.applySnapshot(bybitOrderbookMsg{
		Symbol: "BTCUSDT",
		Bids:   [][2]string{{"100", "1"}, {"102", "1"}, {"101", "1"}},
		Asks:   [][2]string{{"110", "1"}, {"108", "1"}, {"109", "1"}},
	})
	snap := b.top5("BTCUSDT")

	// Bids descending: best (highest) bid first.
	if snap.Bids[0].Price.String() != "102" || snap.Bids[1].Price.String() != "101" || snap.Bids[2].Price.String() != "100" {
		t.Errorf("bids not sorted descending: %+v", snap.Bids)
	}
	// Asks ascending: best (lowest) ask first.
	if snap.Asks[0].Price.String() != "108" || snap.Asks[1].Price.String() != "109" || snap.Asks[2].Price.String() != "110" {
		t.Errorf("asks not sorted ascending: %+v", snap.Asks)
	}
}
