package orderbook

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestOrderBookUpdateHistoryRing(t *testing.T) {
	ring := newBookUpdateRing(3)

	ring.push(BookUpdate{Symbol: "AAPL", Sequence: 1, Bid: decimal.NewFromInt(100), Ask: decimal.NewFromInt(101)})
	ring.push(BookUpdate{Symbol: "AAPL", Sequence: 2, Bid: decimal.NewFromInt(101), Ask: decimal.NewFromInt(102)})
	ring.push(BookUpdate{Symbol: "AAPL", Sequence: 3, Bid: decimal.NewFromInt(102), Ask: decimal.NewFromInt(103)})
	ring.push(BookUpdate{Symbol: "AAPL", Sequence: 4, Bid: decimal.NewFromInt(103), Ask: decimal.NewFromInt(104)})

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
		{Symbol: "AAPL", Active: true, MinQty: decimal.NewFromInt(1), StepSize: decimal.NewFromInt(1)},
		{Symbol: "MSFT", Active: true, MinQty: decimal.NewFromInt(1), StepSize: decimal.NewFromInt(1)},
	})

	if err := ob.RecordUpdate(BookUpdate{Symbol: "AAPL", Sequence: 10, Bid: decimal.NewFromInt(190), Ask: decimal.NewFromInt(191)}); err != nil {
		t.Fatalf("record update 10: %v", err)
	}
	if err := ob.RecordUpdate(BookUpdate{Symbol: "AAPL", Sequence: 11, Bid: decimal.NewFromInt(191), Ask: decimal.NewFromInt(192)}); err != nil {
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
		MinQty:      decimal.NewFromInt(1),
		MaxQty:      decimal.NewFromInt(10),
		StepSize:    decimal.NewFromInt(1),
		MinNotional: decimal.NewFromInt(100),
	})

	result := ob.Validate(ProposedOrder{
		OrchestratorID: "acct-1",
		Symbol:    "NVDA",
		Side:      SideBuy,
		Qty:       decimal.NewFromFloat(2.7),
		Price:     decimal.NewFromInt(60),
	})
	if !result.Valid {
		t.Fatalf("expected order to validate, got invalid: %+v", result)
	}
	if !result.AdjustedQty.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("expected adjusted qty 2, got %s", result.AdjustedQty)
	}

	unsupported := ob.Validate(ProposedOrder{
		OrchestratorID: "acct-1",
		Symbol:    "AMD",
		Side:      SideBuy,
		Qty:       decimal.NewFromInt(1),
		Price:     decimal.NewFromInt(100),
	})
	if unsupported.Valid {
		t.Fatalf("expected unsupported symbol to fail: %+v", unsupported)
	}
}
