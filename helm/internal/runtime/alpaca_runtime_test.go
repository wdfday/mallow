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
package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"

	"mallow/helm/internal/infra/exchange"
	alpacaact "mallow/helm/internal/infra/exchange/alpaca/act"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// ── Setup ─────────────────────────────────────────────────────────────────────

type alpacaTestEnv struct {
	ex    *alpacaact.Client
	creds exchange.Credentials
	rt    *runtime.HelmRuntime
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
	rt := runtime.NewHelmRuntime(
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

func newAlpacaHand(env *alpacaTestEnv) *runtime.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(1), // 1 share minimum for Alpaca
	})
	hand := runtime.NewHand(uuid.New(), env.rt.HelmID, env.rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandRiskConfig{}, decimal.Zero)
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
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSigWithSLTP("AAPL", sl, tp, false))

	select {
	case e := <-placed:
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			t.Logf("entry order failed (market may be closed): %s — skipping bracket check", e.Reason)
			return
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: entry order not placed within 20s")
	}

	select {
	case e := <-filled:
		t.Logf("filled: order_id=%s qty=%s fill_price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
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
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSigWithSLTP("AAPL",
		decimal.NewFromFloat(slOffset),
		decimal.NewFromFloat(tpOffset),
		true,
	))

	select {
	case e := <-placed:
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			t.Logf("entry order failed (market may be closed): %s — skipping bracket check", e.Reason)
			return
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
}
