// E2E tests: signal → order placement pipeline.
//
// Tests that Hand.run() correctly processes a DeliverSignal call and submits
// an order to the real exchange sandbox/demo API.
//
// Each sub-test skips when its credentials env-vars are not set.
//
// Environment variables:
//
//	BINANCE_API_KEY / BINANCE_API_SECRET     (demo-api.binance.com)
//	OKX_API_KEY / OKX_API_SECRET / OKX_PASSPHRASE (simulated trading)
//	BYBIT_API_KEY / BYBIT_API_SECRET         (api-demo.bybit.com)
//	ALPACA_API_KEY / ALPACA_API_SECRET       (paper-api.alpaca.markets)
//
// go test -v -run TestSignalToOrder ./internal/runtime/ -timeout 60s
package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	alpacaact "mallow/helm/internal/infra/exchange/alpaca/act"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	bybitact "mallow/helm/internal/infra/exchange/bybit/act"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// fillNotify watches the hand's activity ring and sends the first
// CodeOrderFilled entry to the returned channel.
func fillNotify(hand *runtime.Hand, timeout time.Duration) <-chan runtime.ActivityEntry {
	ch := make(chan runtime.ActivityEntry, 1)
	deadline := time.Now().Add(timeout)
	go func() {
		for time.Now().Before(deadline) {
			for _, e := range hand.Activity() {
				if e.Code == runtime.CodeOrderFilled {
					ch <- e
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	return ch
}

// orderNotify watches the hand's activity ring and sends the first
// CodeOrderPlaced or CodeOrderFailed entry to the returned channel.
// The test selects on that channel with a timeout rather than polling inline.
func orderNotify(hand *runtime.Hand, timeout time.Duration) <-chan runtime.ActivityEntry {
	ch := make(chan runtime.ActivityEntry, 1)
	deadline := time.Now().Add(timeout)
	go func() {
		for time.Now().Before(deadline) {
			for _, e := range hand.Activity() {
				if e.Code == runtime.CodeOrderPlaced || e.Code == runtime.CodeOrderFailed {
					ch <- e
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	return ch
}

// newTestRuntime builds a minimal HelmRuntime with a seeded portfolio.
// capital is the starting cash balance (e.g. 100000 USDT).
func newTestRuntime(ex exchange.Exchange, creds exchange.Credentials, capital decimal.Decimal) *runtime.HelmRuntime {
	pf := portfolio.New(capital)
	rm := risk.New(risk.DefaultConfig(), pf)
	ob := orderbook.NewOrderBook(ex.Name())
	return runtime.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		ex.Name(), pf, rm, ob, ex, creds, nil,
	)
}

// newFixedQtyHand creates a Hand with FixedQty sizing (no poslog — dev mode).
func newFixedQtyHand(rt *runtime.HelmRuntime, qty decimal.Decimal) *runtime.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: qty,
	})
	return runtime.NewHand(uuid.New(), rt.HelmID, rt, strat, tact, false, 1, 0, nil)
}

// longSig builds a long entry signal with strength 1.0.
func longSig(symbol string) runtime.Signal {
	return strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirLong,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	}
}

// cancelAllOrders cancels any pending orders found in the hand after a test.
func cancelAllOrders(t *testing.T, rt *runtime.HelmRuntime, hand *runtime.Hand) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, o := range hand.Orders() {
		switch o.Status {
		case "new", "accepted", "pending_new", "partially_filled", "submitted", "open":
			if err := rt.Exchange.CancelOrder(ctx, rt.Creds, o.ID); err != nil {
				t.Logf("cleanup CancelOrder %s: %v (non-fatal)", o.ID, err)
			} else {
				t.Logf("cleanup: cancelled order %s", o.ID)
			}
		}
	}
}

// ── Binance Demo ──────────────────────────────────────────────────────────────

func TestSignalToOrder_Binance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set in creds_test.go")
	}

	const symbol = "BTCUSDT"
	ex := binanceact.New(true) // testnet → demo-api.binance.com
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	// Fetch live price so ProcessTrade can size the fixed-qty order.
	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.UpdatePrice(symbol, price)
		t.Logf("current %s price: %s", symbol, price)
	} else {
		t.Logf("price fetch failed (%v) — ProcessTrade will fetch inline", err)
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer hand.Stop()

	notify := orderNotify(hand, 15*time.Second)

	hand.DeliverSignal(longSig(symbol))

	select {
	case entry := <-notify:
		t.Logf("result: code=%d symbol=%s side=%s qty=%s price=%s order_id=%s reason=%s",
			entry.Code, entry.Symbol, entry.Side, entry.Qty, entry.Price, entry.OrderID, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			t.Errorf("order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout: no order placed or failed within 15s")
	}

	orders := hand.Orders()
	if len(orders) == 0 {
		t.Fatal("hand.Orders() is empty after signal")
	}
	t.Logf("orders[0]: id=%s status=%s filled_qty=%s filled_avg=%s",
		orders[0].ID, orders[0].Status, orders[0].FilledQty, orders[0].FilledAvg)

	cancelAllOrders(t, rt, hand)
}

// ── OKX Simulated ─────────────────────────────────────────────────────────────

func TestSignalToOrder_OKX(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if okxPaperAPIKey == "" {
		t.Skip("OKX paper credentials not set in creds_test.go")
	}

	const symbol = "BTC-USDT"
	ex := okxact.New(okxact.Config{Paper: true})
	creds := exchange.Credentials{APIKey: okxPaperAPIKey, APISecret: okxPaperAPISecret, Passphrase: okxPaperPassphrase}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.UpdatePrice(symbol, price)
		t.Logf("current %s price: %s", symbol, price)
	} else {
		t.Logf("price fetch failed (%v) — ProcessTrade will fetch inline", err)
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer hand.Stop()

	notify := orderNotify(hand, 15*time.Second)

	hand.DeliverSignal(longSig(symbol))

	select {
	case entry := <-notify:
		t.Logf("result: code=%d symbol=%s side=%s qty=%s price=%s order_id=%s reason=%s",
			entry.Code, entry.Symbol, entry.Side, entry.Qty, entry.Price, entry.OrderID, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			t.Errorf("order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout: no order placed or failed within 15s")
	}

	orders := hand.Orders()
	if len(orders) == 0 {
		t.Fatal("hand.Orders() is empty after signal")
	}
	t.Logf("orders[0]: id=%s status=%s filled_qty=%s filled_avg=%s",
		orders[0].ID, orders[0].Status, orders[0].FilledQty, orders[0].FilledAvg)

	cancelAllOrders(t, rt, hand)
}

// ── Bybit Demo ────────────────────────────────────────────────────────────────

func TestSignalToOrder_Bybit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if bybitTestAPIKey == "" {
		t.Skip("Bybit demo credentials not set in creds_test.go")
	}

	const symbol = "BTCUSDT"
	ex := bybitact.New(bybitact.Config{Paper: true})
	creds := exchange.Credentials{APIKey: bybitTestAPIKey, APISecret: bybitTestAPISecret}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	// Bybit does not implement PriceFetcher — seed the price cache manually.
	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.MarkPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.UpdatePrice(symbol, price)
		t.Logf("current %s mark price: %s", symbol, price)
	} else {
		t.Logf("mark price fetch failed (%v) — using approximate seed", err)
		rt.UpdatePrice(symbol, decimal.NewFromFloat(65_000))
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer hand.Stop()

	notify := orderNotify(hand, 15*time.Second)

	hand.DeliverSignal(longSig(symbol))

	select {
	case entry := <-notify:
		t.Logf("result: code=%d symbol=%s side=%s qty=%s price=%s order_id=%s reason=%s",
			entry.Code, entry.Symbol, entry.Side, entry.Qty, entry.Price, entry.OrderID, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			t.Errorf("order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout: no order placed or failed within 15s")
	}

	orders := hand.Orders()
	if len(orders) == 0 {
		t.Fatal("hand.Orders() is empty after signal")
	}
	t.Logf("orders[0]: id=%s status=%s filled_qty=%s filled_avg=%s",
		orders[0].ID, orders[0].Status, orders[0].FilledQty, orders[0].FilledAvg)

	cancelAllOrders(t, rt, hand)
}

// ── Alpaca Paper ──────────────────────────────────────────────────────────────

func TestSignalToOrder_Alpaca(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if alpacaPaperAPIKey == "" {
		t.Skip("Alpaca paper credentials not set in creds_test.go")
	}

	const symbol = "AAPL"
	ex := alpacaact.New(alpacaact.Config{})
	creds := exchange.Credentials{APIKey: alpacaPaperAPIKey, APISecret: alpacaPaperAPISecret}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.UpdatePrice(symbol, price)
		t.Logf("current %s price: %s", symbol, price)
	} else {
		t.Logf("price fetch failed (%v) — ProcessTrade will fetch inline", err)
	}

	// Alpaca requires markets open for US equities market orders.
	// Use 1 share — minimum whole-share quantity.
	hand := newFixedQtyHand(rt, decimal.NewFromFloat(1))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer hand.Stop()

	notify := orderNotify(hand, 15*time.Second)

	hand.DeliverSignal(longSig(symbol))

	select {
	case entry := <-notify:
		t.Logf("result: code=%d symbol=%s side=%s qty=%s price=%s order_id=%s reason=%s",
			entry.Code, entry.Symbol, entry.Side, entry.Qty, entry.Price, entry.OrderID, entry.Reason)
		// Market orders on Alpaca paper may fail outside trading hours; log but don't fail the test.
		if entry.Code == runtime.CodeOrderFailed {
			t.Logf("order not placed: %s (market may be closed)", entry.Reason)
			return
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout: no order placed or failed within 15s")
	}

	orders := hand.Orders()
	if len(orders) == 0 {
		t.Fatal("hand.Orders() is empty after signal")
	}
	t.Logf("orders[0]: id=%s status=%s filled_qty=%s filled_avg=%s",
		orders[0].ID, orders[0].Status, orders[0].FilledQty, orders[0].FilledAvg)

	cancelAllOrders(t, rt, hand)
}
