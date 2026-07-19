package portfolio_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor/core/portfolio"
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

// TestApplySync_ExchangeEquityStoredSeparately verifies ApplySync stores the
// exchange-reported equity as a distinct snapshot (ExchangeEquity) without
// altering the live, continuously-computed Equity() — a stale sync-time value
// must never override what cash+positions currently compute to.
func TestApplySync_ExchangeEquityStoredSeparately(t *testing.T) {
	p := portfolio.New(d("10000"))

	p.ApplySync(
		d("9500"),
		[]portfolio.SyncedPosition{
			{Symbol: "BTCUSDT", Qty: d("1"), AvgPrice: d("100"), CurPrice: d("110")},
		},
		d("9700"), // exchange's own margin-adjusted equity — deliberately different from cash+mv
		nil,
		decimal.Zero,
	)

	if !p.ExchangeEquity().Equal(d("9700")) {
		t.Errorf("ExchangeEquity: expected 9700, got %s", p.ExchangeEquity())
	}
	// Live Equity() = cash(9500) + qty(1)*curPrice(110) = 9610 — NOT 9700.
	if !p.Equity().Equal(d("9610")) {
		t.Errorf("Equity: expected live-computed 9610 (not the exchange snapshot), got %s", p.Equity())
	}
}

// TestApplySync_NoExchangeEquity verifies a zero exchangeEquity (exchanges
// without a margin concept, e.g. Binance spot) reads back as zero, not as a
// fallback to the computed value — callers must do their own fallback.
func TestApplySync_NoExchangeEquity(t *testing.T) {
	p := portfolio.New(d("10000"))

	p.ApplySync(
		d("10000"),
		nil,
		decimal.Zero, // not reported
		nil,
		decimal.Zero,
	)

	if !p.ExchangeEquity().IsZero() {
		t.Errorf("expected ExchangeEquity zero when not reported, got %s", p.ExchangeEquity())
	}
	if !p.Equity().Equal(d("10000")) {
		t.Errorf("expected live Equity 10000, got %s", p.Equity())
	}
}

// TestApplySync_BalancesPassthrough verifies per-asset balances round-trip
// through ApplySync/Balances() without affecting Cash()-driven sizing.
func TestApplySync_BalancesPassthrough(t *testing.T) {
	p := portfolio.New(d("10000"))

	p.ApplySync(
		d("9000"),
		nil,
		decimal.Zero,
		[]portfolio.Balance{
			{Asset: "USDT", Free: d("9000"), Locked: d("500")},
			{Asset: "BNB", Free: d("2"), Locked: decimal.Zero},
		},
		decimal.Zero,
	)

	balances := p.Balances()
	if len(balances) != 2 {
		t.Fatalf("expected 2 balances, got %d", len(balances))
	}
	if !balances[0].Locked.Equal(d("500")) {
		t.Errorf("expected USDT locked 500, got %s", balances[0].Locked)
	}
	// Balances is informational — Cash() must still reflect the sync's cash param, not Balances.
	if !p.Cash().Equal(d("9000")) {
		t.Errorf("expected cash 9000 unaffected by Balances passthrough, got %s", p.Cash())
	}
}

// TestApplySync_MarginRatioPassthrough verifies the margin ratio round-trips
// for the risk-gate consumer, and defaults to zero ("no data") when absent.
func TestApplySync_MarginRatioPassthrough(t *testing.T) {
	p := portfolio.New(d("10000"))

	p.ApplySync(d("10000"), nil, decimal.Zero, nil, d("0.85"))
	if !p.MarginRatio().Equal(d("0.85")) {
		t.Errorf("expected margin ratio 0.85, got %s", p.MarginRatio())
	}

	p.ApplySync(d("10000"), nil, decimal.Zero, nil, decimal.Zero)
	if !p.MarginRatio().IsZero() {
		t.Errorf("expected margin ratio reset to zero when a later sync omits it, got %s", p.MarginRatio())
	}
}

func TestSyncBalance_UpsertsNewAsset(t *testing.T) {
	p := portfolio.New(d("10000"))

	p.SyncBalance("BNB", d("2.5"))

	balances := p.Balances()
	if len(balances) != 1 {
		t.Fatalf("expected 1 balance, got %d", len(balances))
	}
	if balances[0].Asset != "BNB" || !balances[0].Free.Equal(d("2.5")) {
		t.Errorf("expected BNB free=2.5, got %+v", balances[0])
	}
}

func TestSyncBalance_UpdatesExistingAsset(t *testing.T) {
	p := portfolio.New(d("10000"))
	p.ApplySync(d("10000"), nil, decimal.Zero,
		[]portfolio.Balance{{Asset: "BNB", Free: d("2"), Locked: d("1")}},
		decimal.Zero,
	)

	p.SyncBalance("BNB", d("3.5"))

	balances := p.Balances()
	if len(balances) != 1 {
		t.Fatalf("expected 1 balance, got %d", len(balances))
	}
	if !balances[0].Free.Equal(d("3.5")) {
		t.Errorf("expected free updated to 3.5, got %s", balances[0].Free)
	}
	// Locked came from the last REST sync and isn't part of a balance-push event — must survive untouched.
	if !balances[0].Locked.Equal(d("1")) {
		t.Errorf("expected locked unchanged at 1, got %s", balances[0].Locked)
	}
}

func TestSyncBalance_DoesNotTouchCash(t *testing.T) {
	p := portfolio.New(d("10000"))

	p.SyncBalance("USDT", d("500"))

	if !p.Cash().Equal(d("10000")) {
		t.Errorf("expected cash unaffected by SyncBalance, got %s", p.Cash())
	}
}

func TestSyncLivePosition_CreatesNewPosition(t *testing.T) {
	p := portfolio.New(d("10000"))
	now := time.Now()

	skipped := p.SyncLivePosition("BTCUSDT", d("0.5"), d("50000"), d("100"), now)

	if skipped {
		t.Fatal("expected skippedZero=false for a non-zero qty")
	}
	pos := p.GetPosition("BTCUSDT")
	if pos == nil {
		t.Fatal("expected position to be created")
	}
	if !pos.Qty.Equal(d("0.5")) || !pos.AvgPrice.Equal(d("50000")) || !pos.UnrealizedPnL.Equal(d("100")) {
		t.Errorf("unexpected position state: %+v", pos)
	}
}

func TestSyncLivePosition_UpdatesExistingPosition(t *testing.T) {
	p := portfolio.New(d("10000"))
	p.ApplySync(d("10000"),
		[]portfolio.SyncedPosition{{Symbol: "BTCUSDT", Qty: d("0.5"), AvgPrice: d("49000"), CurPrice: d("50000")}},
		decimal.Zero, nil, decimal.Zero,
	)

	p.SyncLivePosition("BTCUSDT", d("0.8"), d("49500"), d("250"), time.Now())

	pos := p.GetPosition("BTCUSDT")
	if !pos.Qty.Equal(d("0.8")) || !pos.AvgPrice.Equal(d("49500")) || !pos.UnrealizedPnL.Equal(d("250")) {
		t.Errorf("unexpected position state after live sync: %+v", pos)
	}
	// CurrentPrice is driven by UpdatePrice from market ticks, not this call — must survive untouched.
	if !pos.CurrentPrice.Equal(d("50000")) {
		t.Errorf("expected CurrentPrice unchanged at 50000, got %s", pos.CurrentPrice)
	}
}

func TestSyncLivePosition_ZeroQtyDoesNotDelete(t *testing.T) {
	p := portfolio.New(d("10000"))
	p.ApplySync(d("10000"),
		[]portfolio.SyncedPosition{{Symbol: "BTCUSDT", Qty: d("0.5"), AvgPrice: d("49000"), CurPrice: d("50000")}},
		decimal.Zero, nil, decimal.Zero,
	)

	skipped := p.SyncLivePosition("BTCUSDT", decimal.Zero, decimal.Zero, decimal.Zero, time.Now())

	if !skipped {
		t.Fatal("expected skippedZero=true for a zero qty")
	}
	pos := p.GetPosition("BTCUSDT")
	if pos == nil {
		t.Fatal("expected existing position to survive a zero-qty WS push (closing is the reconciler's job, not this method's)")
	}
	if !pos.Qty.Equal(d("0.5")) {
		t.Errorf("expected qty unchanged at 0.5, got %s", pos.Qty)
	}
}
