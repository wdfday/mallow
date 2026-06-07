package portfolio_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/runtime/core/portfolio"
)

func d(s string) decimal.Decimal {
	val, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return val
}

func TestApplyFill_Long(t *testing.T) {
	p := portfolio.New(d("10000"))
	now := time.Now()

	// 1. Buy to open 2 units at $100
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now,
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideBuy,
		Qty:        d("2"),
		Price:      d("100"),
		Commission: d("0.5"),
	})

	pos := p.GetPosition("BTCUSDT")
	if pos == nil {
		t.Fatal("expected position to exist")
	}
	if !pos.Qty.Equal(d("2")) {
		t.Errorf("expected qty 2, got %s", pos.Qty)
	}
	if !pos.AvgPrice.Equal(d("100")) {
		t.Errorf("expected avg price 100, got %s", pos.AvgPrice)
	}
	// cash = 10000 - (2*100 + 0.5) = 9799.5
	if !p.Cash().Equal(d("9799.5")) {
		t.Errorf("expected cash 9799.5, got %s", p.Cash())
	}

	// 2. Buy to add 1 unit at $110
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now.Add(time.Minute),
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideBuy,
		Qty:        d("1"),
		Price:      d("110"),
		Commission: d("0.2"),
	})

	pos = p.GetPosition("BTCUSDT")
	// new avg price = (2*100 + 1*110)/3 = 103.333333333
	expectedAvg := d("103.3333333333333333") // using decimal equality for verification
	if pos.AvgPrice.Sub(expectedAvg).Abs().GreaterThan(d("1e-9")) {
		t.Errorf("expected avg price around 103.3333, got %s", pos.AvgPrice)
	}
	if !pos.Qty.Equal(d("3")) {
		t.Errorf("expected qty 3, got %s", pos.Qty)
	}
	// cash = 9799.5 - (1*110 + 0.2) = 9689.3
	if !p.Cash().Equal(d("9689.3")) {
		t.Errorf("expected cash 9689.3, got %s", p.Cash())
	}

	// 3. Sell to reduce 2 units at $120
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now.Add(2 * time.Minute),
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideSell,
		Qty:        d("2"),
		Price:      d("120"),
		Commission: d("0.4"),
	})

	pos = p.GetPosition("BTCUSDT")
	if !pos.Qty.Equal(d("1")) {
		t.Errorf("expected qty 1, got %s", pos.Qty)
	}
	// Avg price remains 103.3333
	if pos.AvgPrice.Sub(expectedAvg).Abs().GreaterThan(d("1e-9")) {
		t.Errorf("expected avg price to remain around 103.3333, got %s", pos.AvgPrice)
	}
	// cash = 9689.3 + (2*120 - 0.4) = 9928.9
	if !p.Cash().Equal(d("9928.9")) {
		t.Errorf("expected cash 9928.9, got %s", p.Cash())
	}

	// Check trades recorded
	trades := p.Trades()
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	trade := trades[0]
	if !trade.Qty.Equal(d("2")) {
		t.Errorf("expected trade qty 2, got %s", trade.Qty)
	}
	// pnl = (120 - 103.3333333333) * 2 - 0.4666666667 (proportional entry) - 0.4 (exit) = 32.4666666667
	expectedPnL := d("32.4666666666666667")
	if trade.PnL.Sub(expectedPnL).Abs().GreaterThan(d("1e-9")) {
		t.Errorf("expected trade PnL around 32.4666, got %s", trade.PnL)
	}

	// 4. Sell to close remaining 1 unit at $130
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now.Add(3 * time.Minute),
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideSell,
		Qty:        d("1"),
		Price:      d("130"),
		Commission: d("0.1"),
	})

	pos = p.GetPosition("BTCUSDT")
	if pos != nil {
		t.Errorf("expected position to be deleted/flat, got %v", pos)
	}
	// cash = 9928.9 + (1*130 - 0.1) = 10058.8
	if !p.Cash().Equal(d("10058.8")) {
		t.Errorf("expected cash 10058.8, got %s", p.Cash())
	}

	trades = p.Trades()
	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(trades))
	}
}

// TestApplyFill_PhantomSell verifies spot behavior: a SELL fill on an empty position
// (phantom sell — exchange fill arrived after Sync already cleared the position)
// must credit cash and NOT create a negative (short) position.
func TestApplyFill_PhantomSell(t *testing.T) {
	p := portfolio.New(d("10000"))
	now := time.Now()

	// Sell 2 units at $100 with no prior position (phantom / orphaned fill).
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now,
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideSell,
		Qty:        d("2"),
		Price:      d("100"),
		Commission: d("0.5"),
	})

	// Spot: no short position must be created.
	pos := p.GetPosition("BTCUSDT")
	if pos != nil {
		t.Errorf("spot: phantom sell must not create a position, got qty=%s", pos.Qty)
	}
	// Cash is still credited (proceeds arrived even though we have no tracking).
	// cash = 10000 + (2*100 - 0.5) = 10199.5
	if !p.Cash().Equal(d("10199.5")) {
		t.Errorf("expected cash 10199.5, got %s", p.Cash())
	}
	// No trades recorded (can't compute PnL without entry price).
	if n := len(p.Trades()); n != 0 {
		t.Errorf("expected 0 trades for phantom sell, got %d", n)
	}
}

// TestApplyFill_OversellClamped verifies spot behavior: selling more units than
// currently held must close the position at actual held qty (no short created).
// This mirrors the race where portfolio lags behind exchange fill qty due to
// lot-size rounding or a partial Sync that cleared avg_price.
func TestApplyFill_OversellClamped(t *testing.T) {
	p := portfolio.New(d("10000"))
	now := time.Now()

	// 1. Long position: Buy 1 unit at 100.
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now,
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideBuy,
		Qty:        d("1"),
		Price:      d("100"),
		Commission: d("0"),
	})
	// cash = 10000 - 100 = 9900
	if !p.Cash().Equal(d("9900")) {
		t.Errorf("after buy: expected cash 9900, got %s", p.Cash())
	}

	// 2. Sell 3 units at 110 — overshoot by 2. Spot: closes 1 unit, no short.
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now.Add(time.Minute),
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideSell,
		Qty:        d("3"),
		Price:      d("110"),
		Commission: d("0"),
	})

	// Position must be flat (closed at qty=1, the max we held).
	pos := p.GetPosition("BTCUSDT")
	if pos != nil {
		t.Errorf("spot: oversell must close to flat, not create short; got qty=%s", pos.Qty)
	}
	// cash = 9900 + (3*110) = 9900 + 330 = 10230
	// Note: cash is credited for the full fill.Qty (exchange already settled 3 units).
	if !p.Cash().Equal(d("10230")) {
		t.Errorf("after oversell: expected cash 10230, got %s", p.Cash())
	}

	// 1 trade recorded: entry at 100, exit at 110, qty=1 (clamped to held qty).
	trades := p.Trades()
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	if !trades[0].Qty.Equal(d("1")) {
		t.Errorf("expected trade qty 1 (clamped), got %s", trades[0].Qty)
	}
	// pnl = (110 - 100) * 1 - 0 commission = 10
	if !trades[0].PnL.Equal(d("10")) {
		t.Errorf("expected PnL 10, got %s", trades[0].PnL)
	}
}
