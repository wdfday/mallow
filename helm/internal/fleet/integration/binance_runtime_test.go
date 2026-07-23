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
package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"

	"mallow/helm/internal/fleet/actor"
	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/risk"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/core/tactics"
	"mallow/helm/internal/infra/exchange"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
)

// ── Setup ─────────────────────────────────────────────────────────────────────

type binanceTestEnv struct {
	ex    *binanceact.Client
	creds exchange.Credentials
	rt    *actor.HelmRuntime
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
	rt := actor.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		ex.Name(), pf, rm, ex, creds, nil, time.Now(),
	)
	rt.FilterStore = &mockFilterStore{}

	// Start the real WS user-data stream, same as production (Registry.Spawn always
	// calls StartStreaming — these bare test runtimes previously never did, so every
	// fill depended entirely on the 30s REST poll ticker, AND Binance market orders
	// that fill synchronously in the PlaceOrder ack never get poll-detected at all
	// (fetchPendingOrders skips anything not in {new,accepted,pending_new,
	// partially_filled,submitted} — an ack already reporting "filled" is invisible to
	// it). Without WS running, those fills were never observed by the hand at all.
	// rt.Stop() (registered below) already cancels this via fillDrainCancel.
	rt.StartStreaming(context.Background())

	// Seed live price for sizing.
	var price decimal.Decimal
	if p, err := ex.GetCurrentPrice(ctx, creds, "ETHUSDT"); err == nil && p.IsPositive() {
		rt.MarketData.SetPrice("ETHUSDT", p)
		price = p
		t.Logf("ETH price seeded: %s", p)
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
		Symbol: "ETHUSDT",
		Side:   exchange.Buy,
		Type:   exchange.Market,
		Qty:    decimal.NewFromFloat(0.01),
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
		orders, err := env.ex.ListOpenOrders(ctx, env.creds, "ETHUSDT")
		cancel()
		if err == nil && len(orders) >= 2 {
			return orders
		}
		time.Sleep(time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	orders, _ := env.ex.ListOpenOrders(ctx, env.creds, "ETHUSDT")
	return orders
}

func newBinanceHand(env *binanceTestEnv) *actor.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(0.01),
	})
	hand := actor.NewHand(uuid.New(), env.rt.HelmID, env.rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	hand.Symbol = "ETHUSDT"
	hand.StrategyName = "signal_follower"
	hand.EnableEventSink()
	// Without this, the WS fill router's `r.hands[botID]` lookup misses (helm_streams.go),
	// so a live fill takes the "orphan" path — applied to the portfolio directly but never
	// routed through hand.applyFill. That skips both this hand's own CodeOrderFilled emit
	// (the event this test's fillNotify listens for) and PlaceExitOrders/bracket scheduling
	// entirely, since those only run inside applyFill.
	env.rt.AddHand(hand, &domain.Hand{ID: hand.ID(), HelmID: env.rt.HelmID, Symbols: domain.StringSlice{hand.Symbol}})
	return hand
}

// cleanupBinance cancels all open orders and sells back any warmup ETH.
func cleanupBinance(t *testing.T, ex *binanceact.Client, creds exchange.Credentials, warmupQty decimal.Decimal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Cancel open orders.
	orders, err := ex.ListOpenOrders(ctx, creds, "ETHUSDT")
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

	// Sell back warmup ETH so it doesn't accumulate across test runs.
	if warmupQty.IsPositive() {
		sellCtx, sellCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer sellCancel()
		sellReq := exchange.OrderRequest{
			Symbol: "ETHUSDT",
			Side:   exchange.Sell,
			Type:   exchange.Market,
			Qty:    warmupQty,
		}
		if res, err := ex.PlaceOrder(sellCtx, creds, sellReq); err != nil {
			t.Logf("cleanup warmup sell failed: %v (non-fatal)", err)
		} else {
			t.Logf("cleanup: sold warmup %s ETH (order %s)", warmupQty, res.ID)
		}
	}
}

func longSigWithSLTP(symbol string, stopPrice, targetPrice decimal.Decimal, isOffset bool) actor.Signal {
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
//	→ PlaceOrder (market buy 0.01 ETH)
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
	rec := recordEvents(hand)
	defer rec.dump(t, "TestBinance_AbsoluteSLTP event log")
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 40*time.Second)

	hand.DeliverSignal(longSigWithSLTP("ETHUSDT", sl, tp, false))

	// Wait for entry order placed.
	if e, ok := recvEvent(placed, 20*time.Second); ok {
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			logHandState(t, env.rt, hand, "ETHUSDT")
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, "ETHUSDT")
		t.Fatal("timeout: entry order not placed within 20s")
	}

	// Wait for fill so OCO goroutine has time to launch.
	if e, ok := recvEvent(filled, 40*time.Second); ok {
		t.Logf("filled: order_id=%s qty=%s avg=%s", e.OrderID, e.Qty, e.Price)
	} else {
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
// Offsets: SL = -50 (50 below fill), TP = +100 (100 above fill).
func TestBinance_OffsetSLTP(t *testing.T) {
	env := newBinanceEnv(t)

	const slOffset = -50.0
	const tpOffset = 100.0
	t.Logf("signal offsets: SL%+.0f TP%+.0f (relative to fill)", slOffset, tpOffset)

	hand := newBinanceHand(env)
	rec := recordEvents(hand)
	defer rec.dump(t, "TestBinance_OffsetSLTP event log")
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 40*time.Second)

	hand.DeliverSignal(longSigWithSLTP("ETHUSDT",
		decimal.NewFromFloat(slOffset),
		decimal.NewFromFloat(tpOffset),
		true, // IsOffset=true
	))

	if e, ok := recvEvent(placed, 20*time.Second); ok {
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			logHandState(t, env.rt, hand, "ETHUSDT")
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, "ETHUSDT")
		t.Fatal("timeout: entry order not placed within 20s")
	}

	var fillPrice decimal.Decimal
	if e, ok := recvEvent(filled, 40*time.Second); ok {
		fillPrice = e.Price
		t.Logf("filled: order_id=%s qty=%s fill_price=%s", e.OrderID, e.Qty, fillPrice)
	} else {
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
	openOrders, err := env.ex.ListOpenOrders(ctx, env.creds, "ETHUSDT")
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

	const symbol = "ETHUSDT"

	// Create a pyramid hand with maxUnits = 3.
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(0.01),
	})

	hand := actor.NewHand(
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
		domain.HandGuardConfig{},
		decimal.Zero,
	)
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.EnableEventSink()
	env.rt.AddHand(hand, &domain.Hand{ID: hand.ID(), HelmID: env.rt.HelmID, Symbols: domain.StringSlice{hand.Symbol}})

	rec := recordEvents(hand)
	defer rec.dump(t, "TestBinance_PyramidAndKill event log")
	hand.Start()
	defer func() {
		if hand.IsRunning() {
			hand.Stop()
		}
	}()

	// Deliver 1st signal.
	placed1 := orderNotify(hand, 20*time.Second)
	filled1 := fillNotify(hand, 40*time.Second)
	hand.DeliverSignal(longSigWithSLTP(symbol, decimal.Zero, decimal.Zero, false))

	var fill1OrderID string
	var fill1Qty decimal.Decimal
	if e, ok := recvEvent(placed1, 20*time.Second); ok {
		t.Logf("1st order placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			t.Fatalf("1st order failed: %s", e.Reason)
		}
	} else {
		t.Fatal("timeout: 1st order not placed")
	}

	if e, ok := recvEvent(filled1, 40*time.Second); ok {
		fill1OrderID = e.OrderID
		fill1Qty = e.Qty
		t.Logf("1st fill: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	} else {
		t.Fatal("timeout: 1st fill not observed")
	}

	// Verify position in portfolio exists.
	pos := env.rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected positive position after 1st fill")
	}
	t.Logf("position after 1st fill: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Deliver 2nd signal (Pyramid Add). The avg-anchor gate only adds to a winning leg
	// (price beyond the blended avg), so nudge the known price above the entry avg first —
	// a live tick rarely moves on its own within the test window.
	env.rt.MarketData.SetPrice(symbol, pos.AvgPrice.Mul(decimal.NewFromFloat(1.001)))
	placed2 := orderNotifyNew(hand, fill1OrderID, 20*time.Second)
	filled2 := fillNotify(hand, 40*time.Second)
	hand.DeliverSignal(longSigWithSLTP(symbol, decimal.Zero, decimal.Zero, false))

	if e, ok := recvEvent(placed2, 20*time.Second); ok {
		t.Logf("2nd order placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			t.Fatalf("2nd order failed: %s", e.Reason)
		}
	} else {
		t.Fatal("timeout: 2nd order not placed")
	}

	var fill2Qty decimal.Decimal
	if e, ok := recvEvent(filled2, 40*time.Second); ok {
		fill2Qty = e.Qty
		t.Logf("2nd fill: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	} else {
		t.Fatal("timeout: 2nd fill not observed")
	}

	// Verify accumulated position exists (an absolute >= threshold is safe here — an
	// unrelated account balance can only push this reading UP, never mask a real
	// entry not having happened).
	pos = env.rt.Portfolio.GetPosition(symbol)
	if pos == nil || pos.Qty.LessThan(decimal.NewFromFloat(0.019)) {
		t.Fatalf("expected accumulated position size >= 0.019, got %v", pos)
	}
	t.Logf("accumulated position: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Baseline right before Kill, not at test start: newBinanceEnv's warmup buy is
	// placed directly via ex.PlaceOrder (bypassing hand/runtime order tracking), so it
	// may not register in Portfolio at all until SOME tracked fill (like this test's
	// own entries above) triggers the debounced full resync (helm_trading.go
	// MarkSyncDirty) for the first time — a baseline captured at the very top of the
	// test can read a stale/zero Portfolio that hasn't caught up yet. Give that resync
	// time to settle here before reading, so both sides of the Kill comparison are
	// equally "settled" reads.
	time.Sleep(5 * time.Second)
	preKillQty := portfolioQty(env.rt, symbol)
	t.Logf("portfolio qty before Kill: %s", preKillQty)

	killCtx, killCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer killCancel()

	t.Log("triggering Hand.Kill()...")
	hand.Kill(killCtx)

	// Wait for the exit fill and its own debounced resync to land, then compare the
	// transition against this hand's own entry fills (ground truth for what Kill must
	// remove) — never against a Portfolio-derived "expected" value (see portfolioQty
	// doc comment).
	time.Sleep(10 * time.Second)
	postKillQty := portfolioQty(env.rt, symbol)
	drop := preKillQty.Sub(postKillQty)
	expectedDrop := fill1Qty.Add(fill2Qty)
	t.Logf("portfolio qty after Kill: %s (dropped by %s, expected ~%s)", postKillQty, drop, expectedDrop)

	tolerance := decimal.NewFromFloat(0.0005) // truncateQty/commission rounding slack
	if drop.Sub(expectedDrop).Abs().GreaterThan(tolerance) {
		t.Errorf("expected qty to drop by ~%s (this hand's accumulated entries) after Kill, got a drop of %s", expectedDrop, drop)
	} else {
		t.Logf("position successfully flattened after Kill! ✓ (dropped %s, expected ~%s)", drop, expectedDrop)
	}

	// Verify hand state is stopped and status is HealthKilled.
	if hand.IsRunning() {
		t.Error("expected hand to be stopped after Kill")
	}
	h := hand.Health()
	if h.Status != actor.HealthKilled {
		t.Errorf("expected hand health status to be HealthKilled, got %s", h.Status)
	}
}

// TestBinance_Release verifies Hand.Release(ctx): the hand performs a synthetic
// close (internal portfolio only — no exchange order placed) and cancels the
// exchange-side SL/TP bracket, but leaves the actual base-asset position alone at
// the exchange. This is the "orphan positions for manual management" exit path,
// distinct from Kill (which flattens for real). Not covered anywhere else at the
// runtime/integration level.
func TestBinance_Release(t *testing.T) {
	env := newBinanceEnv(t)
	if env.price.IsZero() {
		t.Skip("price not available — cannot compute SL/TP")
	}

	const symbol = "ETHUSDT"
	sl := env.price.Mul(decimal.NewFromFloat(0.97)).Round(2)
	tp := env.price.Mul(decimal.NewFromFloat(1.05)).Round(2)

	hand := newBinanceHand(env)
	rec := recordEvents(hand)
	defer rec.dump(t, "TestBinance_Release event log")
	hand.Start()
	defer func() {
		if hand.IsRunning() {
			hand.Stop()
		}
	}()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 40*time.Second)
	hand.DeliverSignal(longSigWithSLTP(symbol, sl, tp, false))

	if e, ok := recvEvent(placed, 20*time.Second); ok {
		t.Logf("entry placed: order_id=%s qty=%s", e.OrderID, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			logHandState(t, env.rt, hand, "ETHUSDT")
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, "ETHUSDT")
		t.Fatal("timeout: entry order not placed")
	}

	var entryQty decimal.Decimal
	if e, ok := recvEvent(filled, 40*time.Second); ok {
		entryQty = e.Qty
		t.Logf("entry filled: qty=%s price=%s", e.Qty, e.Price)
	} else {
		logHandState(t, env.rt, hand, "ETHUSDT")
		t.Fatal("timeout: entry fill not observed")
	}
	if !entryQty.IsPositive() {
		t.Fatal("expected positive fill qty from entry")
	}

	// Give the bracket-placement goroutine time to land the SL/TP orders so
	// Release has something real to cancel.
	waitOCO(t, env, 20*time.Second)

	// Baseline right before Release, not at test start (see the analogous comment in
	// TestBinance_PyramidAndKill: newBinanceEnv's warmup buy is placed outside hand/
	// runtime tracking, so Portfolio may not reflect it — or any resync at all — until
	// SOME tracked fill triggers the debounced full resync for the first time).
	// waitOCO can return as soon as the bracket appears (~1s), which is faster than
	// the 3s debounce window — sleep past it here so preReleaseQty is a settled
	// reading, not a stale pre-resync one (that's what caused the huge post-Release
	// jump when the resync finally landed AFTER the post-reading instead of before).
	time.Sleep(5 * time.Second)
	preReleaseQty := portfolioQty(env.rt, symbol)
	t.Logf("portfolio qty before Release: %s", preReleaseQty)

	ordersPlacedBefore := hand.Metrics().OrdersPlaced

	t.Log("triggering Hand.Release()...")
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer releaseCancel()
	hand.Release(releaseCtx)

	logHandState(t, env.rt, hand, "ETHUSDT")

	if hand.IsRunning() {
		t.Error("expected hand to be stopped after Release")
	}
	if h := hand.Health(); h.Status != actor.HealthReleased {
		t.Errorf("expected hand health status to be HealthReleased, got %s", h.Status)
	}

	// Release does NOT sell — qty should be unchanged from right before Release (asset
	// stays put at the exchange). Asserting "flat" would assert the wrong thing here
	// (that's what Kill does, not Release).
	time.Sleep(10 * time.Second)
	postReleaseQty := portfolioQty(env.rt, symbol)
	t.Logf("portfolio qty after Release: %s (was %s before)", postReleaseQty, preReleaseQty)
	tolerance := decimal.NewFromFloat(0.0005)
	if postReleaseQty.Sub(preReleaseQty).Abs().GreaterThan(tolerance) {
		t.Errorf("expected qty unchanged by Release (asset stays at the exchange), before=%s after=%s", preReleaseQty, postReleaseQty)
	} else {
		t.Log("qty unchanged by Release — entry still held at the exchange, as expected ✓")
	}

	// Release must NOT place a real flatten order at the exchange — metrics.OrdersPlaced
	// should be unchanged (only the exchange-side bracket cancel happens, no new order).
	if got := hand.Metrics().OrdersPlaced; got != ordersPlacedBefore {
		t.Errorf("expected OrdersPlaced unchanged by Release (no exchange order), before=%d after=%d", ordersPlacedBefore, got)
	}

	// The SL/TP bracket should have been cancelled as safety cleanup.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	openOrders, err := env.ex.ListOpenOrders(ctx, env.creds, "ETHUSDT")
	if err != nil {
		t.Logf("ListOpenOrders after release: %v (non-fatal)", err)
	} else {
		t.Logf("open orders after release: %d (expect 0 — bracket cancelled)", len(openOrders))
		for _, o := range openOrders {
			t.Logf("  order: id=%s side=%s status=%s qty=%s", o.ID, o.Side, o.Status, o.Qty)
		}
		if len(openOrders) != 0 {
			t.Errorf("expected bracket orders cancelled after Release, found %d still open", len(openOrders))
		}
	}

	t.Logf("NOTE: %s ETH remains physically held at the exchange (Release does not sell it) — cleanup below only cancels orders, it does not sell this back.", entryQty)
}

// TestBinance_ExitSignalCancelsBracket verifies that when a manual exit signal
// closes a position that already has a live exchange-side SL/TP bracket, the
// bracket gets cancelled via cancelExitOrders (applyExitFill path) instead of
// being left dangling. Not covered by TestSignalRoundTrip_Binance (signal_e2e_test.go),
// which enters/exits without ever placing a bracket.
func TestBinance_ExitSignalCancelsBracket(t *testing.T) {
	env := newBinanceEnv(t)
	if env.price.IsZero() {
		t.Skip("price not available — cannot compute SL/TP")
	}

	const symbol = "ETHUSDT"
	sl := env.price.Mul(decimal.NewFromFloat(0.97)).Round(2)
	tp := env.price.Mul(decimal.NewFromFloat(1.05)).Round(2)

	hand := newBinanceHand(env)
	rec := recordEvents(hand)
	defer rec.dump(t, "TestBinance_ExitSignalCancelsBracket event log")
	hand.Start()
	defer func() {
		if hand.IsRunning() {
			hand.Stop()
		}
	}()

	entryPlaced := orderNotify(hand, 20*time.Second)
	entryFilled := fillNotify(hand, 40*time.Second)
	hand.DeliverSignal(longSigWithSLTP(symbol, sl, tp, false))

	var entryOrderID string
	if e, ok := recvEvent(entryPlaced, 20*time.Second); ok {
		entryOrderID = e.OrderID
		t.Logf("entry placed: order_id=%s qty=%s", e.OrderID, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			logHandState(t, env.rt, hand, symbol)
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: entry order not placed")
	}

	if _, ok := recvEvent(entryFilled, 40*time.Second); !ok {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: entry fill not observed")
	}

	// Give the bracket-placement goroutine time to land the SL/TP orders.
	bracketOrders := waitOCO(t, env, 20*time.Second)
	if len(bracketOrders) < 2 {
		t.Skipf("bracket did not land (got %d legs) — cannot verify cancellation of something that isn't there", len(bracketOrders))
	}
	t.Logf("bracket confirmed live: %d orders", len(bracketOrders))

	// Baseline right before the exit signal, not at test start (see the analogous
	// comment in TestBinance_PyramidAndKill on why "test start" reads a stale/zero
	// Portfolio before any tracked fill has triggered the first debounced resync).
	// waitOCO can return as soon as the bracket appears (~1s) — faster than the 3s
	// debounce window — so sleep past it here to get a settled reading (see the
	// analogous comment in TestBinance_Release on the jump this caused otherwise).
	time.Sleep(5 * time.Second)
	preExitQty := portfolioQty(env.rt, symbol)
	t.Logf("portfolio qty before exit signal: %s", preExitQty)

	// Deliver a manual exit signal — should place a market sell, and on fill,
	// cancelExitOrders should cancel the still-live SL/TP bracket. "Closed" here just
	// means this hand's own exit order received its fill (fillNotifyOrder) — no need
	// to chase CodePositionClosed or env.rt.Portfolio for that: Portfolio is helm-wide
	// and only eventually consistent with the real (shared-account) exchange balance,
	// so it's not a reliable signal for "did THIS exit fill" on any tight timeline.
	exitNotify := orderNotifyNew(hand, entryOrderID, 15*time.Second)
	hand.DeliverSignal(exitSig(symbol))

	var exitOrderID string
	if e, ok := recvEvent(exitNotify, 15*time.Second); ok {
		exitOrderID = e.OrderID
		t.Logf("exit placed: order_id=%s qty=%s", e.OrderID, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			logHandState(t, env.rt, hand, symbol)
			t.Fatalf("exit order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: exit order not placed")
	}

	var exitFillQty decimal.Decimal
	if e, ok := recvEvent(fillNotifyOrder(hand, exitOrderID, 30*time.Second), 30*time.Second); ok {
		exitFillQty = e.Qty
		t.Logf("exit filled: qty=%s price=%s", e.Qty, e.Price)
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: exit fill not observed")
	}

	// Give cancelExitOrders (called from applyExitFill) time to actually reach the exchange.
	time.Sleep(4 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	openOrders, err := env.ex.ListOpenOrders(ctx, env.creds, symbol)
	if err != nil {
		t.Logf("ListOpenOrders after exit: %v (non-fatal)", err)
	} else {
		t.Logf("open orders after exit signal: %d (expect 0 — bracket must be cancelled, not left dangling)", len(openOrders))
		for _, o := range openOrders {
			t.Logf("  order: id=%s side=%s status=%s qty=%s", o.ID, o.Side, o.Status, o.Qty)
		}
		if len(openOrders) != 0 {
			t.Errorf("expected SL/TP bracket cancelled after exit signal closed the position, found %d still open", len(openOrders))
		}
	}

	// The real market sell (unlike Release's synthetic close) should remove exactly
	// the exit fill's qty from the portfolio. Note this is NOT necessarily entryQty:
	// the exit qty gets floored to the exchange's step size, so any sub-step residual
	// (e.g. entryQty=0.00999 but step=0.0001 sells only 0.0099) stays behind as dust
	// rather than vanishing — comparing against exitFillQty (not entryQty, and NOT
	// preExitQty itself) is what makes this assertion correct regardless of rounding.
	time.Sleep(6 * time.Second)
	postExitQty := portfolioQty(env.rt, symbol)
	drop := preExitQty.Sub(postExitQty)
	t.Logf("portfolio qty after exit: %s (was %s before, drop=%s, exit fill qty=%s)", postExitQty, preExitQty, drop, exitFillQty)
	tolerance := decimal.NewFromFloat(0.0005)
	if drop.Sub(exitFillQty).Abs().GreaterThan(tolerance) {
		t.Errorf("expected qty to drop by the exit fill's qty %s, got drop=%s (before=%s after=%s)", exitFillQty, drop, preExitQty, postExitQty)
	} else {
		t.Log("position dropped by exactly the exit fill's qty ✓")
	}
}

// TestBinance_OCOExternallyCancelled verifies that when BOTH SL/TP bracket legs are
// cancelled directly at the exchange — bypassing the hand entirely, so neither ID is
// ever added to pendingCancels — HandleExitOrderCanceled treats this as a genuine
// external close (user cancelled the OCO manually from the exchange UI) rather than
// helm-initiated cleanup: it disowns the leg (KindPositionOrphaned, CodePositionExtClosed),
// books no realized PnL, and does NOT sell anything (the asset stays at the exchange).
// Not covered by TestBinance_ExitSignalCancelsBracket, where the cancel IS helm-initiated
// (via cancelExitOrders, IDs pre-marked in pendingCancels).
func TestBinance_OCOExternallyCancelled(t *testing.T) {
	env := newBinanceEnv(t)
	if env.price.IsZero() {
		t.Skip("price not available — cannot compute SL/TP")
	}

	const symbol = "ETHUSDT"
	sl := env.price.Mul(decimal.NewFromFloat(0.97)).Round(2)
	tp := env.price.Mul(decimal.NewFromFloat(1.05)).Round(2)

	hand := newBinanceHand(env)
	rec := recordEvents(hand)
	defer rec.dump(t, "TestBinance_OCOExternallyCancelled event log")
	hand.Start()
	defer func() {
		if hand.IsRunning() {
			hand.Stop()
		}
	}()

	entryPlaced := orderNotify(hand, 20*time.Second)
	entryFilled := fillNotify(hand, 40*time.Second)
	hand.DeliverSignal(longSigWithSLTP(symbol, sl, tp, false))

	if e, ok := recvEvent(entryPlaced, 20*time.Second); ok {
		t.Logf("entry placed: order_id=%s qty=%s", e.OrderID, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			logHandState(t, env.rt, hand, symbol)
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: entry order not placed")
	}

	if _, ok := recvEvent(entryFilled, 40*time.Second); !ok {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: entry fill not observed")
	}

	// Give the bracket-placement goroutine time to land the SL/TP orders.
	bracketOrders := waitOCO(t, env, 20*time.Second)
	if len(bracketOrders) < 2 {
		t.Skipf("bracket did not land (got %d legs) — cannot verify external-cancel handling of something that isn't there", len(bracketOrders))
	}
	t.Logf("bracket confirmed live: %d orders", len(bracketOrders))

	if legs := hand.ActiveLegs(); len(legs) == 0 {
		t.Fatal("expected an active leg before simulating external cancel")
	}

	// Settle past the 3s debounce window so this baseline isn't a stale pre-resync
	// reading (see the analogous comment in TestBinance_Release).
	time.Sleep(5 * time.Second)
	preCancelQty := portfolioQty(env.rt, symbol)
	t.Logf("portfolio qty before external cancel: %s", preCancelQty)

	extClosed := codeNotify(hand, actor.CodePositionExtClosed, 20*time.Second)

	// Cancel BOTH bracket legs directly against the exchange, bypassing the hand
	// entirely (never goes through cancelExitOrders, so neither ID is ever marked in
	// pendingCancels) — simulating a user manually cancelling the OCO from the
	// exchange UI. Binance auto-cancels the OCO sibling as a side effect of cancelling
	// either leg, so the second call here is expected to (harmlessly) find it already gone.
	cancelCtx, cancelCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelCancel()
	for _, o := range bracketOrders {
		if err := env.ex.CancelOrder(cancelCtx, env.creds, o.ID); err != nil {
			t.Logf("direct cancel of bracket leg %s: %v (non-fatal — Binance OCO auto-cancels the sibling)", o.ID, err)
		}
	}

	if e, ok := recvEvent(extClosed, 20*time.Second); ok {
		t.Logf("external close detected: reason=%s", e.Reason)
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: CodePositionExtClosed not observed after cancelling both bracket legs externally")
	}

	if legs := hand.ActiveLegs(); len(legs) != 0 {
		t.Errorf("expected leg disowned (no longer active) after external cancel, still active: %+v", legs)
	}

	// Disowning does NOT sell anything — the asset is left at the exchange (now the
	// user's to manage) — so portfolio qty must be unchanged, unlike a real exit fill.
	time.Sleep(6 * time.Second)
	postCancelQty := portfolioQty(env.rt, symbol)
	t.Logf("portfolio qty after external cancel: %s (was %s before)", postCancelQty, preCancelQty)
	tolerance := decimal.NewFromFloat(0.0005)
	if postCancelQty.Sub(preCancelQty).Abs().GreaterThan(tolerance) {
		t.Errorf("expected qty unchanged by external cancel (asset stays at the exchange), before=%s after=%s", preCancelQty, postCancelQty)
	} else {
		t.Log("qty unchanged after external cancel — position disowned, not sold ✓")
	}

	t.Logf("NOTE: this entry's ETH remains physically held at the exchange (disowned, not sold) — cleanup below only sells back the warmup qty, not this.")
}

// mockFilterStore is a no-op SymbolFilterStore for tests that don't need precision rules.
type mockFilterStore struct{}

func (m *mockFilterStore) GetFilters(_ string) exchange.SymbolFilters {
	return exchange.SymbolFilters{}
}
func (m *mockFilterStore) SetFilters(_ string, _ exchange.SymbolFilters) {}
