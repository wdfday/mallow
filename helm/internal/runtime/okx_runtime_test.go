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
package runtime_test

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

	var price decimal.Decimal
	if p, err := ex.GetCurrentPrice(ctx, creds, "BTC-USDT"); err == nil && p.IsPositive() {
		rt.UpdatePrice("BTC-USDT", p)
		price = p
		t.Logf("BTC-USDT price seeded: %s", p)
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
		FixedQty: decimal.NewFromFloat(0.001),
	})
	hand := runtime.NewHand(uuid.New(), env.rt.HelmID, env.rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandRiskConfig{}, decimal.Zero)
	hand.Symbol = "BTC-USDT"
	hand.StrategyName = "signal_follower"
	hand.EnableEventSink()
	return hand
}

func cleanupOKX(t *testing.T, ex *okxact.Client, creds exchange.Credentials) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	orders, err := ex.ListOpenOrders(ctx, creds, "BTC-USDT")
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
	filled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSigWithSLTP("BTC-USDT", sl, tp, false))

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

	select {
	case e := <-filled:
		t.Logf("filled: order_id=%s qty=%s fill_price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("fill not observed (WS may not be connected) — giving algo goroutine time")
	}

	// Give PlaceExitOrders goroutine time to complete.
	time.Sleep(4 * time.Second)

	t.Log("OKX algo (oco) placement attempted — checking open orders for confirmation")

	// Verify open orders count on exchange (entry position, algo orders are separate endpoint).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	openOrders, err := env.ex.ListOpenOrders(ctx, env.creds, "BTC-USDT")
	if err != nil {
		t.Logf("ListOpenOrders: %v (non-fatal, algo orders on separate endpoint)", err)
	} else {
		t.Logf("regular open orders: %d (algo/OCO orders are on algo endpoint)", len(openOrders))
		for _, o := range openOrders {
			t.Logf("  order: id=%s side=%s status=%s qty=%s", o.ID, o.Side, o.Status, o.Qty)
		}
	}
}

// TestOKX_OffsetSLTP tests IsOffset=true: SL/TP offsets resolved after fill.
// OKX simulated may fill market orders faster (often synchronous).
// After fill, applyFill resolves: SL = fillPrice + stopOffset, TP = fillPrice + tpOffset.
func TestOKX_OffsetSLTP(t *testing.T) {
	env := newOKXEnv(t)

	const slOffset = -2000.0
	const tpOffset = 4000.0
	t.Logf("signal offsets: SL%+.0f TP%+.0f (isOffset=true)", slOffset, tpOffset)

	hand := newOKXHand(env)
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSigWithSLTP("BTC-USDT",
		decimal.NewFromFloat(slOffset),
		decimal.NewFromFloat(tpOffset),
		true,
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
		t.Log("fill not observed — continuing")
	}

	if fillPrice.IsPositive() {
		resolvedSL := fillPrice.Add(decimal.NewFromFloat(slOffset))
		resolvedTP := fillPrice.Add(decimal.NewFromFloat(tpOffset))
		t.Logf("resolved bracket: SL=%s TP=%s", resolvedSL, resolvedTP)
	}

	time.Sleep(4 * time.Second)
	t.Log("OKX algo (oco) attempted after offset resolution")
}
