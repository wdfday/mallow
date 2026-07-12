// Integration tests: OKX Simulated Trading — full HelmRuntime signal → bracket pipeline.
//
// Two bracket scenarios:
//   - isOffset=false: absolute SL/TP → placed inline at order creation (optimization)
//     or post-fill via PlaceExitOrders (current behavior — algo order).
//   - isOffset=true: delta offsets → resolved after fill → PlaceExitOrders algo order.
//
// OKX always uses post-fill order-algo (oco/conditional) for brackets currently.
// If both SL and TP are set → ordType=oco. Only one set → ordType=conditional.
//
// Environment variables required:
//
//	OKX_API_KEY / OKX_API_SECRET / OKX_PASSPHRASE
//
// go test -v -run TestOKX_ ./internal/runtime/ -timeout 90s
package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"

	"mallow/helm/internal/infra/exchange"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// ── Setup ─────────────────────────────────────────────────────────────────────

type okxTestEnv struct {
	ex    *okxact.Client
	creds exchange.Credentials
	rt    *runtime.HelmRuntime
	price decimal.Decimal
}

func newOKXEnv(t *testing.T) *okxTestEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if okxPaperAPIKey == "" {
		t.Skip("OKX paper credentials not set in creds_test.go")
	}

	ex := okxact.New(okxact.Config{Paper: true})
	creds := exchange.Credentials{APIKey: okxPaperAPIKey, APISecret: okxPaperAPISecret, Passphrase: okxPaperPassphrase}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	capital := decimal.NewFromFloat(100_000)
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
	rt := runtime.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		ex.Name(), pf, rm, ex, creds, nil, time.Now(),
	)

	// Start the real WS user-data stream, same as production (Registry.Spawn always
	// calls StartStreaming) — without it, fills only ever reach the hand via the 30s
	// REST poll ticker, and an order whose PlaceOrder ack already reports "filled"
	// (as OKX/Binance can for a fast market fill) is invisible to that poll entirely.
	// rt.Stop() (registered below) already cancels this via fillDrainCancel.
	rt.StartStreaming(context.Background())

	var price decimal.Decimal
	if p, err := ex.GetCurrentPrice(ctx, creds, "ETH-USDT"); err == nil && p.IsPositive() {
		rt.UpdatePrice("ETH-USDT", p)
		price = p
		t.Logf("ETH-USDT price seeded: %s", p)
	} else {
		t.Logf("price fetch failed: %v", err)
	}

	t.Cleanup(func() {
		cleanupOKX(t, ex, creds)
		rt.Stop()
	})

	return &okxTestEnv{ex: ex, creds: creds, rt: rt, price: price}
}

func newOKXHand(env *okxTestEnv) *runtime.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(0.01),
	})
	hand := runtime.NewHand(uuid.New(), env.rt.HelmID, env.rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	hand.Symbol = "ETH-USDT"
	hand.StrategyName = "signal_follower"
	hand.EnableEventSink()
	// Without this, the WS fill router's `r.hands[botID]` lookup misses (helm_streams.go),
	// so a live fill takes the "orphan" path — applied to the portfolio directly but never
	// routed through hand.applyFill. That skips both this hand's own CodeOrderFilled emit
	// (the event this test's fillNotify listens for) and PlaceExitOrders/bracket scheduling
	// entirely, since those only run inside applyFill. See INTEGRATION_TESTS.md.
	env.rt.AddHand(hand, &domain.Hand{ID: hand.ID(), HelmID: env.rt.HelmID, Symbols: domain.StringSlice{hand.Symbol}})
	return hand
}

func cleanupOKX(t *testing.T, ex *okxact.Client, creds exchange.Credentials) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	orders, err := ex.ListOpenOrders(ctx, creds, "ETH-USDT")
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

	// Algo orders (OCO/conditional brackets) live on a separate endpoint from regular
	// orders and won't be caught by ListOpenOrders/CancelOrder above.
	algos, err := ex.ListLiveAlgoOrders(ctx, creds, "ETH-USDT")
	if err != nil {
		t.Logf("cleanup ListLiveAlgoOrders: %v (non-fatal)", err)
		return
	}
	for _, a := range algos {
		if err := ex.CancelOrder(ctx, creds, a.AlgoID); err != nil {
			t.Logf("cleanup CancelOrder (algo) %s: %v (non-fatal)", a.AlgoID, err)
		} else {
			t.Logf("cleanup: cancelled algo order %s (%s sl=%s tp=%s)", a.AlgoID, a.OrdType, a.StopLoss, a.TakeProfit)
		}
	}
}

// checkOKXAlgoOrders queries the live algo-orders endpoint (OCO/conditional) for instID
// and logs what it finds. Unlike ListOpenOrders (regular orders only), this is the
// correct endpoint for verifying a PlaceExitOrders bracket actually landed on OKX.
func checkOKXAlgoOrders(t *testing.T, ex *okxact.Client, creds exchange.Credentials, instID string) []exchange.LiveAlgoOrder {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	algos, err := ex.ListLiveAlgoOrders(ctx, creds, instID)
	if err != nil {
		t.Logf("algo-order check: ListLiveAlgoOrders failed: %v (non-fatal)", err)
		return nil
	}
	t.Logf("algo-order check: %d live algo order(s) for %s", len(algos), instID)
	for _, a := range algos {
		t.Logf("  algo order: id=%s type=%s sl=%s tp=%s", a.AlgoID, a.OrdType, a.StopLoss, a.TakeProfit)
	}
	return algos
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestOKX_AbsoluteSLTP tests IsOffset=false: absolute SL/TP prices.
// After fill, PlaceExitOrders places an OKX order-algo (ordType=oco).
// The algo order does not appear in ListOpenOrders (regular orders) —
// it appears in algo-orders endpoint. The test verifies PlaceExitOrders
// is called without error by checking no CodeOrderFailed activity after fill.
func TestOKX_AbsoluteSLTP(t *testing.T) {
	env := newOKXEnv(t)
	if env.price.IsZero() {
		t.Skip("price not available — cannot compute absolute SL/TP")
	}

	sl := env.price.Mul(decimal.NewFromFloat(0.97)).Round(1)
	tp := env.price.Mul(decimal.NewFromFloat(1.05)).Round(1)
	t.Logf("signal SL=%s TP=%s (absolute, isOffset=false)", sl, tp)

	hand := newOKXHand(env)
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 40*time.Second)

	hand.DeliverSignal(longSigWithSLTP("ETH-USDT", sl, tp, false))

	if e, ok := recvEvent(placed, 20*time.Second); ok {
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			logHandState(t, env.rt, hand, "ETH-USDT")
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, "ETH-USDT")
		t.Fatal("timeout: entry order not placed within 20s")
	}

	if e, ok := recvEvent(filled, 40*time.Second); ok {
		t.Logf("filled: order_id=%s qty=%s fill_price=%s", e.OrderID, e.Qty, e.Price)
	} else {
		t.Log("fill not observed (WS may not be connected) — giving algo goroutine time")
	}

	// Give PlaceExitOrders goroutine time to complete.
	time.Sleep(4 * time.Second)

	// The algo-orders endpoint (not ListOpenOrders — that's regular orders only) is
	// the correct place to verify an OCO bracket landed on OKX.
	algos := checkOKXAlgoOrders(t, env.ex, env.creds, "ETH-USDT")
	if len(algos) == 0 {
		t.Error("expected ≥1 live algo (oco) order after fill, got 0")
	}

	logHandState(t, env.rt, hand, "ETH-USDT")
}

// TestOKX_OffsetSLTP tests IsOffset=true: SL/TP offsets resolved after fill.
// OKX simulated may fill market orders faster (often synchronous).
// After fill, applyFill resolves: SL = fillPrice + stopOffset, TP = fillPrice + tpOffset.
func TestOKX_OffsetSLTP(t *testing.T) {
	env := newOKXEnv(t)

	const slOffset = -50.0
	const tpOffset = 100.0
	t.Logf("signal offsets: SL%+.0f TP%+.0f (isOffset=true)", slOffset, tpOffset)

	hand := newOKXHand(env)
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 40*time.Second)

	hand.DeliverSignal(longSigWithSLTP("ETH-USDT",
		decimal.NewFromFloat(slOffset),
		decimal.NewFromFloat(tpOffset),
		true,
	))

	if e, ok := recvEvent(placed, 20*time.Second); ok {
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			logHandState(t, env.rt, hand, "ETH-USDT")
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, "ETH-USDT")
		t.Fatal("timeout: entry order not placed within 20s")
	}

	var fillPrice decimal.Decimal
	if e, ok := recvEvent(filled, 40*time.Second); ok {
		fillPrice = e.Price
		t.Logf("filled: order_id=%s qty=%s fill_price=%s", e.OrderID, e.Qty, fillPrice)
	} else {
		t.Log("fill not observed — continuing")
	}

	if fillPrice.IsPositive() {
		resolvedSL := fillPrice.Add(decimal.NewFromFloat(slOffset))
		resolvedTP := fillPrice.Add(decimal.NewFromFloat(tpOffset))
		t.Logf("resolved bracket: SL=%s TP=%s", resolvedSL, resolvedTP)
	}

	time.Sleep(4 * time.Second)

	algos := checkOKXAlgoOrders(t, env.ex, env.creds, "ETH-USDT")
	if len(algos) == 0 {
		t.Error("expected ≥1 live algo (oco) order after fill, got 0")
	}
	logHandState(t, env.rt, hand, "ETH-USDT")
}

// TestOKX_PyramidAndKill mirrors TestBinance_PyramidAndKill: verifies that a
// pyramid-enabled hand accepts a 2nd entry on the same symbol up to maxUnits, and
// that Kill(ctx) flattens the accumulated spot position synchronously. Parity
// coverage for OKX — the bracket/offset tests above only exercise a single entry.
func TestOKX_PyramidAndKill(t *testing.T) {
	env := newOKXEnv(t)
	if env.price.IsZero() {
		t.Skip("price not available — cannot compute pyramid sizes")
	}

	const symbol = "ETH-USDT"

	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(0.01),
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
		domain.HandGuardConfig{},
		decimal.Zero,
	)
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.EnableEventSink()
	env.rt.AddHand(hand, &domain.Hand{ID: hand.ID(), HelmID: env.rt.HelmID, Symbols: domain.StringSlice{hand.Symbol}})

	hand.Start()
	defer func() {
		if hand.IsRunning() {
			hand.Stop()
		}
	}()

	// 1st entry.
	placed1 := orderNotify(hand, 20*time.Second)
	filled1 := fillNotify(hand, 40*time.Second)
	hand.DeliverSignal(longSigWithSLTP(symbol, decimal.Zero, decimal.Zero, false))

	var fill1OrderID string
	var fill1Qty decimal.Decimal
	if e, ok := recvEvent(placed1, 20*time.Second); ok {
		t.Logf("1st order placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			logHandState(t, env.rt, hand, symbol)
			t.Fatalf("1st order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: 1st order not placed")
	}

	if e, ok := recvEvent(filled1, 40*time.Second); ok {
		fill1OrderID = e.OrderID
		fill1Qty = e.Qty
		t.Logf("1st fill: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: 1st fill not observed")
	}

	pos := env.rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected positive position after 1st fill")
	}
	t.Logf("position after 1st fill: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// 2nd entry (pyramid add). The avg-anchor gate only adds to a winning leg, so
	// nudge the known price above the entry avg first (a live tick rarely moves
	// on its own within the test window).
	env.rt.UpdatePrice(symbol, pos.AvgPrice.Mul(decimal.NewFromFloat(1.001)))
	placed2 := orderNotifyNew(hand, fill1OrderID, 20*time.Second)
	filled2 := fillNotify(hand, 40*time.Second)
	hand.DeliverSignal(longSigWithSLTP(symbol, decimal.Zero, decimal.Zero, false))

	if e, ok := recvEvent(placed2, 20*time.Second); ok {
		t.Logf("2nd order placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			logHandState(t, env.rt, hand, symbol)
			t.Fatalf("2nd order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: 2nd order not placed")
	}

	var fill2Qty decimal.Decimal
	if e, ok := recvEvent(filled2, 40*time.Second); ok {
		fill2Qty = e.Qty
		t.Logf("2nd fill: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: 2nd fill not observed")
	}

	// Absolute >= threshold is safe here — an unrelated account balance can only
	// push this reading UP, never mask a real entry not having happened.
	pos = env.rt.Portfolio.GetPosition(symbol)
	if pos == nil || pos.Qty.LessThan(decimal.NewFromFloat(0.019)) {
		t.Fatalf("expected accumulated position size >= 0.019, got %v", pos)
	}
	t.Logf("accumulated position: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Baseline right before Kill, not at test start (see the analogous comment in
	// TestBinance_PyramidAndKill: newOKXEnv's account balance may not be reflected in
	// Portfolio at all until some tracked fill triggers the debounced full resync for
	// the first time — a baseline captured at the very top of the test can read a
	// stale/zero Portfolio that hasn't caught up yet).
	time.Sleep(5 * time.Second)
	preKillQty := portfolioQty(env.rt, symbol)
	t.Logf("portfolio qty before Kill: %s", preKillQty)

	// Trigger KILL to emergency flatten the accumulated position.
	killCtx, killCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer killCancel()

	t.Log("triggering Hand.Kill()...")
	hand.Kill(killCtx)

	// Wait for the exit fill and its own debounced resync to land, then compare the
	// transition against this hand's own entry fills (ground truth for what Kill must
	// remove) — never against a Portfolio-derived "expected" value (portfolioQty doc
	// comment).
	time.Sleep(10 * time.Second)
	postKillQty := portfolioQty(env.rt, symbol)
	drop := preKillQty.Sub(postKillQty)
	expectedDrop := fill1Qty.Add(fill2Qty)
	t.Logf("portfolio qty after Kill: %s (dropped by %s, expected ~%s)", postKillQty, drop, expectedDrop)

	logHandState(t, env.rt, hand, symbol)
	tolerance := decimal.NewFromFloat(0.0005) // truncateQty/commission rounding slack
	if drop.Sub(expectedDrop).Abs().GreaterThan(tolerance) {
		t.Errorf("expected qty to drop by ~%s (this hand's accumulated entries) after Kill, got a drop of %s", expectedDrop, drop)
	} else {
		t.Logf("position successfully flattened after Kill! ✓ (dropped %s, expected ~%s)", drop, expectedDrop)
	}

	if hand.IsRunning() {
		t.Error("expected hand to be stopped after Kill")
	}
	h := hand.Health()
	if h.Status != runtime.HealthKilled {
		t.Errorf("expected hand health status to be HealthKilled, got %s", h.Status)
	}
}

// TestOKX_Release verifies Hand.Release(ctx): the hand performs a synthetic close
// (internal portfolio only — no exchange order placed) and cancels the exchange-side
// algo bracket, but leaves the actual base-asset position alone at the exchange.
// Mirrors TestBinance_Release. Not covered anywhere else at the runtime/integration level.
func TestOKX_Release(t *testing.T) {
	env := newOKXEnv(t)
	if env.price.IsZero() {
		t.Skip("price not available — cannot compute SL/TP")
	}

	const symbol = "ETH-USDT"
	sl := env.price.Mul(decimal.NewFromFloat(0.97)).Round(1)
	tp := env.price.Mul(decimal.NewFromFloat(1.05)).Round(1)

	hand := newOKXHand(env)
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
		if e.Code == runtime.CodeOrderFailed {
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

	var entryQty decimal.Decimal
	if e, ok := recvEvent(filled, 40*time.Second); ok {
		entryQty = e.Qty
		t.Logf("entry filled: qty=%s price=%s", e.Qty, e.Price)
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: entry fill not observed")
	}
	if !entryQty.IsPositive() {
		t.Fatal("expected positive fill qty from entry")
	}

	// Give the bracket-placement goroutine time to land the algo order so
	// Release has something real to cancel. This also needs to clear the 3s
	// debounce window (MarkSyncDirty → Sync) so the baseline read right below
	// is settled — a shorter sleep here caused a huge, spurious qty jump on the
	// POST-Release reading instead (the resync landing late; see the analogous
	// comment in TestBinance_Release).
	time.Sleep(5 * time.Second)
	if algos := checkOKXAlgoOrders(t, env.ex, env.creds, symbol); len(algos) == 0 {
		t.Skip("bracket did not land — cannot verify cancellation of something that isn't there")
	}

	// Baseline right before Release, not at test start (see the analogous comment in
	// TestOKX_PyramidAndKill: newOKXEnv's account balance may not be reflected in
	// Portfolio at all until some tracked fill triggers the debounced full resync for
	// the first time; this entry's own fill + the sleep above already gave that time).
	preReleaseQty := portfolioQty(env.rt, symbol)
	t.Logf("portfolio qty before Release: %s", preReleaseQty)

	ordersPlacedBefore := hand.Metrics().OrdersPlaced

	t.Log("triggering Hand.Release()...")
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer releaseCancel()
	hand.Release(releaseCtx)

	logHandState(t, env.rt, hand, symbol)

	if hand.IsRunning() {
		t.Error("expected hand to be stopped after Release")
	}
	if h := hand.Health(); h.Status != runtime.HealthReleased {
		t.Errorf("expected hand health status to be HealthReleased, got %s", h.Status)
	}

	// Release does NOT sell — qty should be unchanged from right before Release (asset
	// stays put at the exchange).
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

	// The algo bracket should have been cancelled as safety cleanup.
	time.Sleep(2 * time.Second)
	if algos := checkOKXAlgoOrders(t, env.ex, env.creds, symbol); len(algos) != 0 {
		t.Errorf("expected algo bracket cancelled after Release, found %d still live", len(algos))
	}

	t.Logf("NOTE: %s ETH remains physically held at the exchange (Release does not sell it) — cleanup below only cancels orders, it does not sell this back.", entryQty)
}

// TestOKX_ExitSignalCancelsBracket verifies that when a manual exit signal closes a
// position that already has a live exchange-side algo bracket, the bracket gets
// cancelled via cancelExitOrders (applyExitFill path) instead of being left dangling.
// Mirrors TestBinance_ExitSignalCancelsBracket.
func TestOKX_ExitSignalCancelsBracket(t *testing.T) {
	env := newOKXEnv(t)
	if env.price.IsZero() {
		t.Skip("price not available — cannot compute SL/TP")
	}

	const symbol = "ETH-USDT"
	sl := env.price.Mul(decimal.NewFromFloat(0.97)).Round(1)
	tp := env.price.Mul(decimal.NewFromFloat(1.05)).Round(1)

	hand := newOKXHand(env)
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
		if e.Code == runtime.CodeOrderFailed {
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

	// Give the bracket-placement goroutine time to land the algo order. This also
	// needs to clear the 3s debounce window (MarkSyncDirty → Sync) so the baseline
	// read right below is settled (see the analogous comment in TestOKX_Release).
	time.Sleep(5 * time.Second)
	if algos := checkOKXAlgoOrders(t, env.ex, env.creds, symbol); len(algos) == 0 {
		t.Skip("bracket did not land — cannot verify cancellation of something that isn't there")
	}

	// Baseline right before the exit signal, not at test start (see the analogous
	// comment in TestOKX_PyramidAndKill).
	preExitQty := portfolioQty(env.rt, symbol)
	t.Logf("portfolio qty before exit signal: %s", preExitQty)

	// Deliver a manual exit signal — should place a market sell, and on fill,
	// cancelExitOrders should cancel the still-live algo bracket. "Closed" here just
	// means this hand's own exit order received its fill (fillNotifyOrder) — no need
	// to chase CodePositionClosed or env.rt.Portfolio for that (see portfolioQty doc
	// comment: helm-wide, only eventually consistent with the real shared-account
	// balance, not a reliable signal on any tight timeline).
	exitNotify := orderNotifyNew(hand, entryOrderID, 15*time.Second)
	hand.DeliverSignal(exitSig(symbol))

	var exitOrderID string
	if e, ok := recvEvent(exitNotify, 15*time.Second); ok {
		exitOrderID = e.OrderID
		t.Logf("exit placed: order_id=%s qty=%s", e.OrderID, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
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

	if algos := checkOKXAlgoOrders(t, env.ex, env.creds, symbol); len(algos) != 0 {
		t.Errorf("expected algo bracket cancelled after exit signal closed the position, found %d still live", len(algos))
	}

	// The real market sell should remove exactly the exit fill's qty from the
	// portfolio. Note this is NOT necessarily entryQty: the exit qty gets floored to
	// the exchange's step size, so any sub-step residual stays behind as dust rather
	// than vanishing — comparing against exitFillQty (not entryQty, and NOT
	// preExitQty itself) is what makes this assertion correct regardless of rounding
	// (see the analogous comment/fix in TestBinance_ExitSignalCancelsBracket).
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
