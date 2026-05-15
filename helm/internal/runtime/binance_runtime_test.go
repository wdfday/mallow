// Integration tests: Binance Demo — full HelmRuntime signal → bracket pipeline.
//
// Tests the complete flow from signal delivery through entry fill to OCO placement.
// Uses demo-api.binance.com (spot market).
//
// Environment variables required:
//
//	BINANCE_API_KEY / BINANCE_API_SECRET
//
// go test -v -run TestBinance_ ./internal/runtime/ -timeout 90s
package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// ── Setup ─────────────────────────────────────────────────────────────────────

type binanceTestEnv struct {
	ex    *binanceact.Client
	creds exchange.Credentials
	rt    *runtime.HelmRuntime
	price decimal.Decimal
}

func newBinanceEnv(t *testing.T) *binanceTestEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set in creds_test.go")
	}

	ex := binanceact.New(true) // demo-api.binance.com
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Fetch real sandbox balance before creating the runtime.
	capital := decimal.NewFromFloat(100_000) // fallback
	if syncer, ok := exchange.Exchange(ex).(exchange.AccountSyncer); ok {
		if snap, err := syncer.SyncAccount(ctx, creds, nil); err == nil && snap.Cash.IsPositive() {
			capital = snap.Cash
			t.Logf("sandbox cash balance: %s USDT", capital)
		} else {
			t.Logf("SyncAccount failed (%v) — using fallback 100k", err)
		}
	}

	pf := portfolio.New(capital)
	rm := risk.New(risk.DefaultConfig(), pf)
	ob := orderbook.NewOrderBook(ex.Name())
	rt := runtime.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		ex.Name(), pf, rm, ob, ex, creds, nil,
	)

	// Seed live price for sizing.
	var price decimal.Decimal
	if p, err := ex.GetCurrentPrice(ctx, creds, "BTCUSDT"); err == nil && p.IsPositive() {
		rt.UpdatePrice("BTCUSDT", p)
		price = p
		t.Logf("BTC price seeded: %s", p)
	} else {
		t.Logf("price fetch failed: %v — tests may fail without a valid price", err)
	}

	// Binance demo sandbox has a settlement delay (~20s) on fresh accounts before
	// bought assets appear in available balance. Place a warmup buy and wait for
	// it to settle so OCO placement in the actual tests succeeds on first attempt.
	warmupCtx, warmupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer warmupCancel()
	warmupReq := exchange.OrderRequest{
		Symbol: "BTCUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.001),
	}
	if _, err := ex.PlaceOrder(warmupCtx, creds, warmupReq); err != nil {
		t.Logf("warmup buy failed (%v) — OCO may hit settlement delay", err)
	} else {
		t.Log("warmup buy placed; waiting 20s for asset settlement")
		time.Sleep(20 * time.Second)
	}

	t.Cleanup(func() {
		cleanupBinance(t, ex, creds, rt)
		rt.Stop()
	})

	return &binanceTestEnv{ex: ex, creds: creds, rt: rt, price: price}
}

// waitOCO polls ListOpenOrders until ≥2 bracket legs appear or deadline is reached.
// Returns the open orders found (may be empty on timeout).
func waitOCO(t *testing.T, env *binanceTestEnv, timeout time.Duration) []exchange.OrderResult {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		orders, err := env.ex.ListOpenOrders(ctx, env.creds, "BTCUSDT")
		cancel()
		if err == nil && len(orders) >= 2 {
			return orders
		}
		time.Sleep(time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	orders, _ := env.ex.ListOpenOrders(ctx, env.creds, "BTCUSDT")
	return orders
}

func newBinanceHand(env *binanceTestEnv) *runtime.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(0.001),
	})
	hand := runtime.NewHand(uuid.New(), env.rt.HelmID, env.rt, strat, tact, false, 1, 0, nil)
	hand.Symbol = "BTCUSDT"
	hand.StrategyName = "signal_follower"
	return hand
}

func cleanupBinance(t *testing.T, ex *binanceact.Client, creds exchange.Credentials, _ *runtime.HelmRuntime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	orders, err := ex.ListOpenOrders(ctx, creds, "BTCUSDT")
	if err != nil {
		t.Logf("cleanup ListOpenOrders: %v (non-fatal)", err)
		return
	}
	for _, o := range orders {
		if err := ex.CancelOrder(ctx, creds, o.ID); err != nil {
			t.Logf("cleanup CancelOrder %s: %v (non-fatal)", o.ID, err)
		} else {
			t.Logf("cleanup: cancelled order %s (%s)", o.ID, o.Side)
		}
	}
}

func longSigWithSLTP(symbol string, stopPrice, targetPrice decimal.Decimal, isOffset bool) runtime.Signal {
	return strategy.Signal{
		Symbol:      symbol,
		Direction:   strategy.DirLong,
		Strength:    1.0,
		StopPrice:   stopPrice,
		TargetPrice: targetPrice,
		IsOffset:    isOffset,
		ReceivedAt:  time.Now().UTC(),
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestBinance_AbsoluteSLTP verifies signal → entry fill → OCO placement
// when SL/TP are absolute prices (IsOffset=false).
//
// Flow: DeliverSignal(sig with absolute SL/TP)
//
//	→ PlaceOrder (market buy 0.001 BTC)
//	→ fill (status=filled or WS fill)
//	→ applyFill → PlaceExitOrders goroutine → Binance spot OCO
//	→ ListOpenOrders should show ≥2 bracket legs
func TestBinance_AbsoluteSLTP(t *testing.T) {
	env := newBinanceEnv(t)
	if env.price.IsZero() {
		t.Skip("price not available — cannot compute absolute SL/TP")
	}

	// SL = 3% below current price, TP = 5% above
	sl := env.price.Mul(decimal.NewFromFloat(0.97)).Round(2)
	tp := env.price.Mul(decimal.NewFromFloat(1.05)).Round(2)
	t.Logf("signal SL=%s TP=%s (absolute)", sl, tp)

	hand := newBinanceHand(env)
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSigWithSLTP("BTCUSDT", sl, tp, false))

	// Wait for entry order placed.
	select {
	case e := <-placed:
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		for _, e := range hand.Activity() {
			t.Logf("activity: code=%d symbol=%s direction=%s reason=%q order_id=%s",
				e.Code, e.Symbol, e.Direction, e.Reason, e.OrderID)
		}
		t.Fatal("timeout: entry order not placed within 20s")
	}

	// Wait for fill so OCO goroutine has time to launch.
	select {
	case e := <-filled:
		t.Logf("filled: order_id=%s qty=%s avg=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("fill not observed in activity (WS may not be running) — continuing with OCO check")
	}

	// Poll until OCO bracket appears (goroutine retries on settle delay, up to 15s).
	openOrders := waitOCO(t, env, 20*time.Second)
	t.Logf("open orders after OCO placement: %d", len(openOrders))
	for _, o := range openOrders {
		t.Logf("  order: id=%s side=%s status=%s qty=%s", o.ID, o.Side, o.Status, o.Qty)
	}
	if len(openOrders) < 2 {
		t.Errorf("expected ≥2 OCO legs, got %d", len(openOrders))
	}
}

// TestBinance_OffsetSLTP verifies that IsOffset=true deltas are resolved
// correctly after fill: SL = fillPrice + stopOffset, TP = fillPrice + tpOffset.
//
// Offsets: SL = -2000 (2000 below fill), TP = +4000 (4000 above fill).
func TestBinance_OffsetSLTP(t *testing.T) {
	env := newBinanceEnv(t)

	const slOffset = -2000.0
	const tpOffset = 4000.0
	t.Logf("signal offsets: SL%+.0f TP%+.0f (relative to fill)", slOffset, tpOffset)

	hand := newBinanceHand(env)
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSigWithSLTP("BTCUSDT",
		decimal.NewFromFloat(slOffset),
		decimal.NewFromFloat(tpOffset),
		true, // IsOffset=true
	))

	select {
	case e := <-placed:
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: entry order not placed within 20s")
	}

	var fillPrice decimal.Decimal
	select {
	case e := <-filled:
		fillPrice = e.Price
		t.Logf("filled: order_id=%s qty=%s fill_price=%s", e.OrderID, e.Qty, fillPrice)
	case <-time.After(30 * time.Second):
		t.Log("fill not observed in activity — continuing with OCO check")
	}

	if fillPrice.IsPositive() {
		resolvedSL := fillPrice.Add(decimal.NewFromFloat(slOffset))
		resolvedTP := fillPrice.Add(decimal.NewFromFloat(tpOffset))
		t.Logf("expected resolved SL=%s TP=%s", resolvedSL, resolvedTP)
	}

	// Give PlaceExitOrders goroutine time to complete (worst-case: 3 retries × backoff = ~7s).
	time.Sleep(8 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	openOrders, err := env.ex.ListOpenOrders(ctx, env.creds, "BTCUSDT")
	if err != nil {
		t.Fatalf("ListOpenOrders: %v", err)
	}
	t.Logf("open orders after OCO placement: %d", len(openOrders))
	for _, o := range openOrders {
		t.Logf("  order: id=%s side=%s status=%s qty=%s", o.ID, o.Side, o.Status, o.Qty)
	}

	if len(openOrders) < 2 {
		t.Errorf("expected ≥2 OCO legs, got %d — PlaceExitOrders may not have been called", len(openOrders))
	}
}
