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
	// pnl = (120 - 103.3333333333) * 2 - 0.4 = 33.333333333 * 2 - 0.4 = 66.666666667 - 0.4 = 66.266666667
	expectedPnL := d("32.9333333333333334") // wait, let's calculate: (120 - 103.3333333333333333) * 2 - 0.4
	// Actually: (120 - 310/3)*2 - 0.4 = (360/3 - 310/3)*2 - 0.4 = (50/3)*2 - 0.4 = 100/3 - 0.4 = 33.33333333 - 0.4 = 32.93333333
	if trade.PnL.Sub(expectedPnL).Abs().GreaterThan(d("1e-9")) {
		t.Errorf("expected trade PnL around 32.9333, got %s", trade.PnL)
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

func TestApplyFill_Short(t *testing.T) {
	p := portfolio.New(d("10000"))
	now := time.Now()

	// 1. Sell to open short position of 2 units at $100
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now,
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideSell,
		Qty:        d("2"),
		Price:      d("100"),
		Commission: d("0.5"),
	})

	pos := p.GetPosition("BTCUSDT")
	if pos == nil {
		t.Fatal("expected position to exist")
	}
	if !pos.Qty.Equal(d("-2")) {
		t.Errorf("expected qty -2, got %s", pos.Qty)
	}
	if !pos.AvgPrice.Equal(d("100")) {
		t.Errorf("expected avg price 100, got %s", pos.AvgPrice)
	}
	// cash = 10000 + (2*100 - 0.5) = 10199.5
	if !p.Cash().Equal(d("10199.5")) {
		t.Errorf("expected cash 10199.5, got %s", p.Cash())
	}

	// 2. Sell to add to short: 1 unit at $90
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now.Add(time.Minute),
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideSell,
		Qty:        d("1"),
		Price:      d("90"),
		Commission: d("0.2"),
	})

	pos = p.GetPosition("BTCUSDT")
	if !pos.Qty.Equal(d("-3")) {
		t.Errorf("expected qty -3, got %s", pos.Qty)
	}
	// new avg price = (2*100 + 1*90)/3 = 96.666666666
	expectedAvg := d("96.6666666666666667")
	if pos.AvgPrice.Sub(expectedAvg).Abs().GreaterThan(d("1e-9")) {
		t.Errorf("expected avg price around 96.6667, got %s", pos.AvgPrice)
	}
	// cash = 10199.5 + (1*90 - 0.2) = 10289.3
	if !p.Cash().Equal(d("10289.3")) {
		t.Errorf("expected cash 10289.3, got %s", p.Cash())
	}

	// 3. Buy to cover/reduce 2 units of short at $80
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now.Add(2 * time.Minute),
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideBuy,
		Qty:        d("2"),
		Price:      d("80"),
		Commission: d("0.4"),
	})

	pos = p.GetPosition("BTCUSDT")
	if !pos.Qty.Equal(d("-1")) {
		t.Errorf("expected qty -1, got %s", pos.Qty)
	}
	// Avg price remains 96.6667
	if pos.AvgPrice.Sub(expectedAvg).Abs().GreaterThan(d("1e-9")) {
		t.Errorf("expected avg price to remain around 96.6667, got %s", pos.AvgPrice)
	}
	// cash = 10289.3 - (2*80 + 0.4) = 10128.9
	if !p.Cash().Equal(d("10128.9")) {
		t.Errorf("expected cash 10128.9, got %s", p.Cash())
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
	// pnl = (96.6666666667 - 80) * 2 - 0.4 = 16.6666666667 * 2 - 0.4 = 33.333333333 - 0.4 = 32.93333333
	expectedPnL := d("32.9333333333333333")
	if trade.PnL.Sub(expectedPnL).Abs().GreaterThan(d("1e-9")) {
		t.Errorf("expected trade PnL around 32.9333, got %s", trade.PnL)
	}

	// 4. Buy to cover/close remaining 1 unit of short at $70
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now.Add(3 * time.Minute),
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideBuy,
		Qty:        d("1"),
		Price:      d("70"),
		Commission: d("0.1"),
	})

	pos = p.GetPosition("BTCUSDT")
	if pos != nil {
		t.Errorf("expected position to be deleted/flat, got %v", pos)
	}
	// cash = 10128.9 - (1*70 + 0.1) = 10058.8
	if !p.Cash().Equal(d("10058.8")) {
		t.Errorf("expected cash 10058.8, got %s", p.Cash())
	}

	trades = p.Trades()
	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(trades))
	}
}

func TestApplyFill_Reversing(t *testing.T) {
	p := portfolio.New(d("10000"))
	now := time.Now()

	// 1. Long position: Buy 1 unit at 100
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now,
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideBuy,
		Qty:        d("1"),
		Price:      d("100"),
		Commission: d("0"),
	})

	// 2. Sell 3 units at 110 (reverses to -2 short)
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now.Add(time.Minute),
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideSell,
		Qty:        d("3"),
		Price:      d("110"),
		Commission: d("0"),
	})

	pos := p.GetPosition("BTCUSDT")
	if pos == nil {
		t.Fatal("expected position to exist")
	}
	if !pos.Qty.Equal(d("-2")) {
		t.Errorf("expected qty -2, got %s", pos.Qty)
	}
	if !pos.AvgPrice.Equal(d("110")) {
		t.Errorf("expected avg price 110, got %s", pos.AvgPrice)
	}

	trades := p.Trades()
	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	if !trades[0].PnL.Equal(d("10")) {
		t.Errorf("expected PnL 10, got %s", trades[0].PnL)
	}

	// 3. Buy 3 units at 105 (reverses to +1 long)
	p.ApplyFill(portfolio.Fill{
		Timestamp:  now.Add(2 * time.Minute),
		HandID:     "bot1",
		Symbol:     "BTCUSDT",
		Side:       portfolio.SideBuy,
		Qty:        d("3"),
		Price:      d("105"),
		Commission: d("0"),
	})

	pos = p.GetPosition("BTCUSDT")
	if pos == nil {
		t.Fatal("expected position to exist")
	}
	if !pos.Qty.Equal(d("1")) {
		t.Errorf("expected qty 1, got %s", pos.Qty)
	}
	if !pos.AvgPrice.Equal(d("105")) {
		t.Errorf("expected avg price 105, got %s", pos.AvgPrice)
	}

	trades = p.Trades()
	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(trades))
	}
	// short entry 110, exit buy 105, qty 2. pnl = (110 - 105) * 2 = 10
	if !trades[1].PnL.Equal(d("10")) {
		t.Errorf("expected second trade PnL 10, got %s", trades[1].PnL)
	}
}
