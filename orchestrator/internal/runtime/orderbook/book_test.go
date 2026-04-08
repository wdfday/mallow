package orderbook

import "testing"

func TestOrderBookUpdateHistoryRing(t *testing.T) {
	ring := newBookUpdateRing(3)

	ring.push(BookUpdate{Symbol: "AAPL", Sequence: 1, Bid: 100, Ask: 101})
	ring.push(BookUpdate{Symbol: "AAPL", Sequence: 2, Bid: 101, Ask: 102})
	ring.push(BookUpdate{Symbol: "AAPL", Sequence: 3, Bid: 102, Ask: 103})
	ring.push(BookUpdate{Symbol: "AAPL", Sequence: 4, Bid: 103, Ask: 104})

	got := ring.snapshot(0)
	if len(got) != 3 {
		t.Fatalf("expected 3 updates, got %d", len(got))
	}
	if got[0].Sequence != 2 || got[1].Sequence != 3 || got[2].Sequence != 4 {
		t.Fatalf("unexpected sequence order: %+v", got)
	}

	latest, ok := ring.latest()
	if !ok {
		t.Fatal("expected latest update")
	}
	if latest.Sequence != 4 {
		t.Fatalf("expected latest sequence 4, got %d", latest.Sequence)
	}
}

func TestOrderBookRecordAndReadRecentUpdates(t *testing.T) {
	ob := NewOrderBook("alpaca")
	ob.RegisterSymbols([]SymbolInfo{
		{Symbol: "AAPL", Active: true, MinQty: 1, StepSize: 1},
		{Symbol: "MSFT", Active: true, MinQty: 1, StepSize: 1},
	})

	if err := ob.RecordUpdate(BookUpdate{Symbol: "AAPL", Sequence: 10, Bid: 190, Ask: 191}); err != nil {
		t.Fatalf("record update 10: %v", err)
	}
	if err := ob.RecordUpdate(BookUpdate{Symbol: "AAPL", Sequence: 11, Bid: 191, Ask: 192}); err != nil {
		t.Fatalf("record update 11: %v", err)
	}

	got := ob.RecentUpdates("AAPL", 1)
	if len(got) != 1 {
		t.Fatalf("expected 1 update, got %d", len(got))
	}
	if got[0].Sequence != 11 {
		t.Fatalf("expected latest sequence 11, got %d", got[0].Sequence)
	}

	supported := ob.SupportedSymbols()
	if len(supported) != 2 || supported[0] != "AAPL" || supported[1] != "MSFT" {
		t.Fatalf("unexpected supported symbols: %+v", supported)
	}
}

func TestOrderBookValidateStillUsesSupportedSymbolState(t *testing.T) {
	ob := NewOrderBook("binance")
	ob.RegisterSymbol(SymbolInfo{
		Symbol:      "NVDA",
		Active:      true,
		MinQty:      1,
		MaxQty:      10,
		StepSize:    1,
		MinNotional: 100,
	})

	result := ob.Validate(ProposedOrder{
		AccountID: "acct-1",
		Symbol:    "NVDA",
		Side:      SideBuy,
		Qty:       2.7,
		Price:     60,
	})
	if !result.Valid {
		t.Fatalf("expected order to validate, got invalid: %+v", result)
	}
	if result.AdjustedQty != 2 {
		t.Fatalf("expected adjusted qty 2, got %f", result.AdjustedQty)
	}

	unsupported := ob.Validate(ProposedOrder{
		AccountID: "acct-1",
		Symbol:    "AMD",
		Side:      SideBuy,
		Qty:       1,
		Price:     100,
	})
	if unsupported.Valid {
		t.Fatalf("expected unsupported symbol to fail: %+v", unsupported)
	}
}
