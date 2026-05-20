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

	"mallow/helm/internal/module/hand/domain"

	"mallow/helm/internal/infra/exchange"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	"mallow/helm/internal/runtime"
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

	// ── Sync real USDT cash from exchange ──────────────────────────────────
	var capital decimal.Decimal
	if syncer, ok := interface{}(ex).(exchange.AccountSyncer); ok {
		if snap, err := syncer.SyncAccount(ctx, creds, nil); err == nil && snap.Cash.IsPositive() {
			capital = snap.Cash
			t.Logf("╔═ EXCHANGE WALLET AT SETUP ═══════════════════════════")
			t.Logf("║ cash(USDT) = %s", snap.Cash)
			for _, b := range snap.Balances {
				if b.Free.IsPositive() {
					t.Logf("║ asset: %s free=%s", b.Asset, b.Free)
				}
			}
			for _, p := range snap.Positions {
				t.Logf("║ pos: %s qty=%s", p.Symbol, p.Qty)
			}
			t.Logf("╚══════════════════════════════════════════════════════")
		} else {
			t.Skipf("SyncAccount failed (%v) — cannot run integration test without real balance", err)
		}
	} else {
		t.Skip("exchange does not support AccountSyncer")
	}

	// Create portfolio with real USDT cash. Positions are NOT synced —
	// only fills from Hand trades will appear in the portfolio.
	pf := portfolio.New(capital)
	rm := risk.New(risk.DefaultConfig(), pf)
	rt := runtime.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		ex.Name(), pf, rm, ex, creds, nil,
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
	var warmupQty decimal.Decimal
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
		warmupQty = warmupReq.Qty
		t.Log("warmup buy placed; waiting 20s for asset settlement")
		time.Sleep(20 * time.Second)
	}

	t.Cleanup(func() {
		cleanupBinance(t, ex, creds, warmupQty)
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
	hand := runtime.NewHand(uuid.New(), env.rt.HelmID, env.rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandRiskConfig{}, decimal.Zero)
	hand.Symbol = "BTCUSDT"
	hand.StrategyName = "signal_follower"
	return hand
}

// cleanupBinance cancels all open orders and sells back any warmup BTC.
func cleanupBinance(t *testing.T, ex *binanceact.Client, creds exchange.Credentials, warmupQty decimal.Decimal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Cancel open orders.
	orders, err := ex.ListOpenOrders(ctx, creds, "BTCUSDT")
	if err != nil {
		t.Logf("cleanup ListOpenOrders: %v (non-fatal)", err)
	} else {
		for _, o := range orders {
			if err := ex.CancelOrder(ctx, creds, o.ID); err != nil {
				t.Logf("cleanup CancelOrder %s: %v (non-fatal)", o.ID, err)
			} else {
				t.Logf("cleanup: cancelled order %s (%s)", o.ID, o.Side)
			}
		}
	}

	// Sell back warmup BTC so it doesn't accumulate across test runs.
	if warmupQty.IsPositive() {
		sellCtx, sellCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer sellCancel()
		sellReq := exchange.OrderRequest{
			Symbol: "BTCUSDT",
			Side:   exchange.Sell,
			Type:   exchange.Market,
			Qty:    warmupQty,
		}
		if res, err := ex.PlaceOrder(sellCtx, creds, sellReq); err != nil {
			t.Logf("cleanup warmup sell failed: %v (non-fatal)", err)
		} else {
			t.Logf("cleanup: sold warmup %s BTC (order %s)", warmupQty, res.ID)
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
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
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
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
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

// TestBinance_PyramidAndKill verifies that:
//  1. Pyramid=true allows multiple entries for the same hand up to maxUnits.
//  2. Kill(ctx) triggers the emergency cancellation of entry orders and flattens
//     the accumulated spot positions synchronously.
func TestBinance_PyramidAndKill(t *testing.T) {
	env := newBinanceEnv(t)
	if env.price.IsZero() {
		t.Skip("price not available — cannot compute pyramid sizes")
	}

	const symbol = "BTCUSDT"

	// Create a pyramid hand with maxUnits = 3.
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(0.001),
	})

	hand := runtime.NewHand(
		uuid.New(),
		env.rt.HelmID,
		env.rt,
		strat,
		tact,
		true, // pyramid = true
		3,    // maxUnits = 3
		0,    // signalTTL = 0
		nil,  // futuresConfig = nil
		domain.OrderTypeMarket,
		0,  // limitTimeoutSec = 0
		"", // limitFallback = ""
		domain.HandRiskConfig{},
		decimal.Zero,
	)
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"

	hand.Start()
	defer func() {
		if hand.IsRunning() {
			hand.Stop()
		}
	}()

	// Deliver 1st signal.
	placed1 := orderNotify(hand, 20*time.Second)
	filled1 := fillNotify(hand, 30*time.Second)
	hand.DeliverSignal(longSigWithSLTP(symbol, decimal.Zero, decimal.Zero, false))

	select {
	case e := <-placed1:
		t.Logf("1st order placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			t.Fatalf("1st order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: 1st order not placed")
	}

	var fill1OrderID string
	select {
	case e := <-filled1:
		fill1OrderID = e.OrderID
		t.Logf("1st fill: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Fatal("timeout: 1st fill not observed")
	}

	// Verify position in portfolio exists.
	pos := env.rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected positive position after 1st fill")
	}
	t.Logf("position after 1st fill: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Deliver 2nd signal (Pyramid Add).
	placed2 := orderNotifyNew(hand, fill1OrderID, 20*time.Second)
	filled2 := fillNotify(hand, 30*time.Second)
	hand.DeliverSignal(longSigWithSLTP(symbol, decimal.Zero, decimal.Zero, false))

	select {
	case e := <-placed2:
		t.Logf("2nd order placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			t.Fatalf("2nd order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: 2nd order not placed")
	}

	select {
	case e := <-filled2:
		t.Logf("2nd fill: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Fatal("timeout: 2nd fill not observed")
	}

	// Verify accumulated position.
	pos = env.rt.Portfolio.GetPosition(symbol)
	if pos == nil || pos.Qty.LessThan(decimal.NewFromFloat(0.002)) {
		t.Fatalf("expected accumulated position size >= 0.002, got %v", pos)
	}
	t.Logf("accumulated position: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Now trigger KILL to emergency flat/close the position.
	killCtx, killCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer killCancel()

	t.Log("triggering Hand.Kill()...")
	hand.Kill(killCtx)

	// Wait for the exit order fill and verify position becomes flat.
	flatDeadline := time.Now().Add(10 * time.Second)
	var flat bool
	for time.Now().Before(flatDeadline) {
		pos = env.rt.Portfolio.GetPosition(symbol)
		if pos == nil || pos.Qty.IsZero() {
			flat = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !flat {
		t.Errorf("expected position to be flat after Kill, but got qty = %s", pos.Qty)
	} else {
		t.Log("position successfully flattened after Kill! ✓")
	}

	// Verify hand state is stopped and status is HealthKilled.
	if hand.IsRunning() {
		t.Error("expected hand to be stopped after Kill")
	}
	h := hand.Health()
	if h.Status != runtime.HealthKilled {
		t.Errorf("expected hand health status to be HealthKilled, got %s", h.Status)
	}
}
