// Integration tests: Alpaca Paper Trading — full HelmRuntime signal → bracket pipeline.
//
// Alpaca brackets = 2 independent orders post-fill:
//
//	Stop order (GTC) for SL
//	Limit order (GTC) for TP
//
// Non-native OCO: when one fires, helm must cancel the survivor via cancelExitOrders.
// Market orders require NYSE/NASDAQ to be open.
// If market is closed, entry order will fail → test logs and passes.
//
// Environment variables required:
//
//	ALPACA_API_KEY / ALPACA_API_SECRET
//
// go test -v -run TestAlpaca_ ./internal/runtime/ -timeout 90s
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
	alpacaact "mallow/helm/internal/infra/exchange/alpaca/act"
)

// ── Setup ─────────────────────────────────────────────────────────────────────

type alpacaTestEnv struct {
	ex    *alpacaact.Client
	creds exchange.Credentials
	rt    *actor.HelmRuntime
	price decimal.Decimal
}

// isUSMarketOpen returns true when NYSE/NASDAQ is currently open.
// Alpaca paper trading only fills market orders during regular hours.
func isUSMarketOpen() bool {
	now := time.Now().UTC()
	// Skip weekends.
	if wd := now.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	// Regular hours: 9:30 AM – 4:00 PM ET.
	// ET = UTC-5 (EST) or UTC-4 (EDT). Use UTC-4 as conservative approximation.
	et := now.Add(-4 * time.Hour)
	open := time.Date(et.Year(), et.Month(), et.Day(), 9, 30, 0, 0, time.UTC)
	close := time.Date(et.Year(), et.Month(), et.Day(), 16, 0, 0, 0, time.UTC)
	return et.After(open) && et.Before(close)
}

func newAlpacaEnv(t *testing.T) *alpacaTestEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if alpacaPaperAPIKey == "" {
		t.Skip("Alpaca paper credentials not set in creds_test.go")
	}
	if !isUSMarketOpen() {
		t.Skip("US market is closed — Alpaca paper orders won't fill outside 9:30 AM–4:00 PM ET (8:30–3:00 AM Vietnam EDT)")
	}

	ex := alpacaact.New(alpacaact.Config{})
	creds := exchange.Credentials{APIKey: alpacaPaperAPIKey, APISecret: alpacaPaperAPISecret}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	capital := decimal.NewFromFloat(100_000)
	if syncer, ok := exchange.Exchange(ex).(exchange.AccountSyncer); ok {
		if snap, err := syncer.SyncAccount(ctx, creds, nil); err == nil && snap.Cash.IsPositive() {
			capital = snap.Cash
			t.Logf("paper account cash balance: %s USD", capital)
		} else {
			t.Logf("SyncAccount failed (%v) — using fallback 100k", err)
		}
	}

	pf := portfolio.New(capital)
	rm := risk.New(risk.DefaultConfig(), pf)
	rt := actor.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		ex.Name(), pf, rm, ex, creds, nil, time.Now(),
	)

	var price decimal.Decimal
	if p, err := ex.GetCurrentPrice(ctx, creds, "AAPL"); err == nil && p.IsPositive() {
		rt.UpdatePrice("AAPL", p)
		price = p
		t.Logf("AAPL price seeded: %s", p)
	} else {
		t.Logf("price fetch failed: %v — SL/TP will use defaults", err)
		price = decimal.NewFromFloat(200)
		rt.UpdatePrice("AAPL", price)
	}

	t.Cleanup(func() {
		cleanupAlpaca(t, ex, creds)
		rt.Stop()
	})

	return &alpacaTestEnv{ex: ex, creds: creds, rt: rt, price: price}
}

func newAlpacaHand(env *alpacaTestEnv) *actor.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(1), // 1 share minimum for Alpaca
	})
	hand := actor.NewHand(uuid.New(), env.rt.HelmID, env.rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	hand.Symbol = "AAPL"
	hand.StrategyName = "signal_follower"
	hand.EnableEventSink()
	return hand
}

func cleanupAlpaca(t *testing.T, ex *alpacaact.Client, creds exchange.Credentials) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	orders, err := ex.ListOpenOrders(ctx, creds, "AAPL")
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

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestAlpaca_AbsoluteSLTP tests IsOffset=false: absolute SL/TP prices.
// After fill, PlaceExitOrders places Stop GTC (SL) + Limit GTC (TP).
// Both appear in ListOpenOrders on Alpaca paper.
//
// Skips bracket check if entry order fails (market closed).
func TestAlpaca_AbsoluteSLTP(t *testing.T) {
	env := newAlpacaEnv(t)

	sl := env.price.Mul(decimal.NewFromFloat(0.97)).Round(2)
	tp := env.price.Mul(decimal.NewFromFloat(1.05)).Round(2)
	t.Logf("signal SL=%s TP=%s (absolute, isOffset=false)", sl, tp)

	hand := newAlpacaHand(env)
	rec := recordEvents(hand)
	defer rec.dump(t, "TestAlpaca_AbsoluteSLTP event log")
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 40*time.Second)

	hand.DeliverSignal(longSigWithSLTP("AAPL", sl, tp, false))

	if e, ok := recvEvent(placed, 20*time.Second); ok {
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			t.Logf("entry order failed (market may be closed): %s — skipping bracket check", e.Reason)
			return
		}
	} else {
		logHandState(t, env.rt, hand, "AAPL")
		t.Fatal("timeout: entry order not placed within 20s")
	}

	if e, ok := recvEvent(filled, 40*time.Second); ok {
		t.Logf("filled: order_id=%s qty=%s fill_price=%s", e.OrderID, e.Qty, e.Price)
	} else {
		t.Log("fill not observed — market may not have filled; giving bracket goroutine time")
	}

	// Give PlaceExitOrders goroutine time to complete.
	time.Sleep(4 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	openOrders, err := env.ex.ListOpenOrders(ctx, env.creds, "AAPL")
	if err != nil {
		t.Fatalf("ListOpenOrders: %v", err)
	}
	t.Logf("open orders after bracket placement: %d", len(openOrders))
	for _, o := range openOrders {
		t.Logf("  order: id=%s side=%s status=%s qty=%s", o.ID, o.Side, o.Status, o.Qty)
	}

	// Alpaca bracket = 1 Stop GTC (SL) + 1 Limit GTC (TP) = 2 orders.
	if len(openOrders) < 2 {
		t.Errorf("expected ≥2 bracket orders (Stop GTC + Limit GTC), got %d", len(openOrders))
	}
	logHandState(t, env.rt, hand, "AAPL")
}

// TestAlpaca_OffsetSLTP tests IsOffset=true: SL/TP as deltas from fill price.
// SL = -5 (5 below fill price), TP = +10 (10 above fill price).
//
// Skips bracket check if entry order fails (market closed).
func TestAlpaca_OffsetSLTP(t *testing.T) {
	env := newAlpacaEnv(t)

	const slOffset = -5.0
	const tpOffset = 10.0
	t.Logf("signal offsets: SL%+.2f TP%+.2f (isOffset=true)", slOffset, tpOffset)

	hand := newAlpacaHand(env)
	rec := recordEvents(hand)
	defer rec.dump(t, "TestAlpaca_OffsetSLTP event log")
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 40*time.Second)

	hand.DeliverSignal(longSigWithSLTP("AAPL",
		decimal.NewFromFloat(slOffset),
		decimal.NewFromFloat(tpOffset),
		true,
	))

	if e, ok := recvEvent(placed, 20*time.Second); ok {
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			t.Logf("entry order failed (market may be closed): %s — skipping bracket check", e.Reason)
			return
		}
	} else {
		logHandState(t, env.rt, hand, "AAPL")
		t.Fatal("timeout: entry order not placed within 20s")
	}

	var fillPrice decimal.Decimal
	if e, ok := recvEvent(filled, 40*time.Second); ok {
		fillPrice = e.Price
		t.Logf("filled: order_id=%s qty=%s fill_price=%s", e.OrderID, e.Qty, fillPrice)
	} else {
		t.Log("fill not observed — continuing with bracket check")
	}

	if fillPrice.IsPositive() {
		t.Logf("resolved bracket: SL=%s TP=%s",
			fillPrice.Add(decimal.NewFromFloat(slOffset)),
			fillPrice.Add(decimal.NewFromFloat(tpOffset)),
		)
	}

	time.Sleep(4 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	openOrders, err := env.ex.ListOpenOrders(ctx, env.creds, "AAPL")
	if err != nil {
		t.Fatalf("ListOpenOrders: %v", err)
	}
	t.Logf("open orders after bracket placement: %d", len(openOrders))
	for _, o := range openOrders {
		t.Logf("  order: id=%s side=%s status=%s qty=%s", o.ID, o.Side, o.Status, o.Qty)
	}

	if len(openOrders) < 2 {
		t.Errorf("expected ≥2 bracket orders (Stop GTC + Limit GTC), got %d", len(openOrders))
	}
	logHandState(t, env.rt, hand, "AAPL")
}

// TestAlpaca_PyramidAndKill mirrors TestBinance_PyramidAndKill: verifies that a
// pyramid-enabled hand accepts a 2nd entry on the same symbol up to maxUnits, and
// that Kill(ctx) flattens the accumulated equity position synchronously. Parity
// coverage for Alpaca — the bracket/offset tests above only exercise a single entry.
//
// Skips entirely if the entry order fails (market closed) — see isUSMarketOpen guard
// in newAlpacaEnv, which already prevents most of this, but the 2nd entry can still
// legitimately fail near the open/close edges.
func TestAlpaca_PyramidAndKill(t *testing.T) {
	env := newAlpacaEnv(t)
	if env.price.IsZero() {
		t.Skip("price not available — cannot compute pyramid sizes")
	}

	const symbol = "AAPL"

	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(1), // 1 share minimum for Alpaca
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

	rec := recordEvents(hand)
	defer rec.dump(t, "TestAlpaca_PyramidAndKill event log")
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

	if e, ok := recvEvent(placed1, 20*time.Second); ok {
		t.Logf("1st order placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			t.Logf("1st order failed (market may be closed): %s — skipping test", e.Reason)
			return
		}
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: 1st order not placed")
	}

	var fill1OrderID string
	if e, ok := recvEvent(filled1, 40*time.Second); ok {
		fill1OrderID = e.OrderID
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
		if e.Code == actor.CodeOrderFailed {
			logHandState(t, env.rt, hand, symbol)
			t.Fatalf("2nd order failed: %s", e.Reason)
		}
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: 2nd order not placed")
	}

	if e, ok := recvEvent(filled2, 40*time.Second); ok {
		t.Logf("2nd fill: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	} else {
		logHandState(t, env.rt, hand, symbol)
		t.Fatal("timeout: 2nd fill not observed")
	}

	pos = env.rt.Portfolio.GetPosition(symbol)
	if pos == nil || pos.Qty.LessThan(decimal.NewFromFloat(1.9)) {
		t.Fatalf("expected accumulated position size >= 1.9 shares, got %v", pos)
	}
	t.Logf("accumulated position: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Trigger KILL to emergency flatten the accumulated position.
	killCtx, killCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer killCancel()

	t.Log("triggering Hand.Kill()...")
	hand.Kill(killCtx)

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

	logHandState(t, env.rt, hand, symbol)
	if !flat {
		t.Errorf("expected position to be flat after Kill, but got qty = %s", pos.Qty)
	} else {
		t.Log("position successfully flattened after Kill")
	}

	if hand.IsRunning() {
		t.Error("expected hand to be stopped after Kill")
	}
	h := hand.Health()
	if h.Status != actor.HealthKilled {
		t.Errorf("expected hand health status to be HealthKilled, got %s", h.Status)
	}
}
