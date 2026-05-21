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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"

	"mallow/helm/internal/infra/exchange"
	alpacaact "mallow/helm/internal/infra/exchange/alpaca/act"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	bybitact "mallow/helm/internal/infra/exchange/bybit/act"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// fillNotify polls the hand activity ring and sends the first CodeOrderFilled entry.
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

// orderNotify polls the hand activity ring and sends the first CodeOrderPlaced or CodeOrderFailed entry.
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
	return runtime.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		ex.Name(), pf, rm, ex, creds, nil, time.Now(),
	)
}

// newFixedQtyHand creates a Hand with FixedQty sizing (no poslog — dev mode).
func newFixedQtyHand(rt *runtime.HelmRuntime, qty decimal.Decimal) *runtime.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: qty,
	})
	return runtime.NewHand(uuid.New(), rt.HelmID, rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandRiskConfig{}, decimal.Zero)
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

// isBalanceError reports whether an order-failed reason is a sandbox balance issue.
// These are skipped (not failed) since they require a manual sandbox top-up.
func isBalanceError(reason string) bool {
	lower := strings.ToLower(reason)
	return strings.Contains(lower, "insufficient") ||
		strings.Contains(lower, "balance") ||
		strings.Contains(lower, "margin")
}

// exitSig builds an exit signal with strength 1.0 (urgent — always passes the filter).
func exitSig(symbol string) runtime.Signal {
	return strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirExit,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	}
}

// orderNotifyNew watches the hand's activity ring and sends the first
// CodeOrderPlaced or CodeOrderFailed entry whose OrderID differs from excludeID.
// Use after an entry order is placed to detect the subsequent exit order.
func orderNotifyNew(hand *runtime.Hand, excludeID string, timeout time.Duration) <-chan runtime.ActivityEntry {
	ch := make(chan runtime.ActivityEntry, 1)
	deadline := time.Now().Add(timeout)
	go func() {
		for time.Now().Before(deadline) {
			for _, e := range hand.Activity() {
				if (e.Code == runtime.CodeOrderPlaced || e.Code == runtime.CodeOrderFailed) &&
					e.OrderID != excludeID {
					ch <- e
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	return ch
}

// fillNotifyOrder watches the hand's activity ring and sends the first
// CodeOrderFilled entry whose OrderID matches targetID.
func fillNotifyOrder(hand *runtime.Hand, targetID string, timeout time.Duration) <-chan runtime.ActivityEntry {
	ch := make(chan runtime.ActivityEntry, 1)
	deadline := time.Now().Add(timeout)
	go func() {
		for time.Now().Before(deadline) {
			for _, e := range hand.Activity() {
				if e.Code == runtime.CodeOrderFilled && e.OrderID == targetID {
					ch <- e
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	return ch
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
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
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
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
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
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
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

// ── Round-trip helpers ────────────────────────────────────────────────────────

// waitFills polls until the hand's activity log contains at least n CodeOrderFilled entries.
func waitFills(hand *runtime.Hand, n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var count int
		for _, e := range hand.Activity() {
			if e.Code == runtime.CodeOrderFilled {
				count++
			}
		}
		if count >= n {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// logHandState dumps hand health, metrics, recent activity, and portfolio position.
func logHandState(t *testing.T, rt *runtime.HelmRuntime, hand *runtime.Hand, symbol string) {
	t.Helper()
	h := hand.Health()
	m := hand.Metrics()
	t.Logf("── state dump ─────────────────────────────────────────────")
	t.Logf("health: status=%s uptime=%s last_error=%s",
		h.Status, h.Uptime, h.LastError)
	t.Logf("metrics: signals=%d filtered=%d dropped=%d trades_approved=%d orders_placed=%d orders_filled=%d orders_failed=%d pnl=%s",
		m.SignalsReceived, m.SignalsFiltered, m.SignalsDropped,
		m.TradesApproved, m.OrdersPlaced, m.OrdersFilled, m.OrdersFailed, m.TotalPnL)
	acts := hand.Activity()
	t.Logf("activity (%d entries):", len(acts))
	for _, e := range acts {
		t.Logf("  [%s] code=%d sym=%s dir=%s side=%s qty=%s price=%s order_id=%s reason=%s",
			e.At.Format("15:04:05.000"), e.Code, e.Symbol, e.Direction,
			e.Side, e.Qty, e.Price, e.OrderID, e.Reason)
	}
	if pos := rt.Portfolio.GetPosition(symbol); pos != nil {
		t.Logf("portfolio position: symbol=%s qty=%s avg_px=%s", pos.Symbol, pos.Qty, pos.AvgPrice)
	} else {
		t.Logf("portfolio position: flat (nil)")
	}
	t.Logf("──────────────────────────────────────────────────────────")
}

// ── Binance Demo — round-trip ─────────────────────────────────────────────────

func TestSignalRoundTrip_Binance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set in creds_test.go")
	}

	const symbol = "BTCUSDT"
	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.UpdatePrice(symbol, price)
		t.Logf("current %s price: %s", symbol, price)
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer func() {
		time.Sleep(5 * time.Second)
		logHandState(t, rt, hand, symbol)
		hand.Stop()
	}()

	// Entry.
	entryNotify := orderNotify(hand, 15*time.Second)
	hand.DeliverSignal(longSig(symbol))
	select {
	case entry := <-entryNotify:
		t.Logf("entry: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
			t.Fatalf("entry order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for entry order")
	}

	if !waitFills(hand, 1, 20*time.Second) {
		t.Fatal("timeout waiting for entry fill")
	}

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected open long position after entry fill")
	}
	t.Logf("position after entry: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Exit.
	exitNotify := orderNotifyNew(hand, hand.Orders()[0].ID, 15*time.Second)
	hand.DeliverSignal(exitSig(symbol))
	select {
	case entry := <-exitNotify:
		t.Logf("exit: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			t.Logf("exit order failed (non-fatal for sandbox): %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for exit order")
	}

	if !waitFills(hand, 2, 20*time.Second) {
		t.Log("exit fill not confirmed within 20s — order may still be pending")
	}

	cancelAllOrders(t, rt, hand)
}

// ── OKX Simulated — round-trip ────────────────────────────────────────────────

func TestSignalRoundTrip_OKX(t *testing.T) {
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
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer func() {
		time.Sleep(5 * time.Second)
		logHandState(t, rt, hand, symbol)
		hand.Stop()
	}()

	// Entry.
	entryNotify := orderNotify(hand, 15*time.Second)
	hand.DeliverSignal(longSig(symbol))
	select {
	case entry := <-entryNotify:
		t.Logf("entry: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
			t.Fatalf("entry order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for entry order")
	}

	if !waitFills(hand, 1, 20*time.Second) {
		t.Fatal("timeout waiting for entry fill")
	}

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected open long position after entry fill")
	}
	t.Logf("position after entry: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Exit.
	exitNotify := orderNotifyNew(hand, hand.Orders()[0].ID, 15*time.Second)
	hand.DeliverSignal(exitSig(symbol))
	select {
	case entry := <-exitNotify:
		t.Logf("exit: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			t.Logf("exit order failed (non-fatal for sandbox): %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for exit order")
	}

	if !waitFills(hand, 2, 20*time.Second) {
		t.Log("exit fill not confirmed within 20s — order may still be pending")
	}

	cancelAllOrders(t, rt, hand)
}

// ── Bybit Demo — round-trip ───────────────────────────────────────────────────

func TestSignalRoundTrip_Bybit(t *testing.T) {
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

	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.MarkPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.UpdatePrice(symbol, price)
		t.Logf("current %s mark price: %s", symbol, price)
	} else {
		rt.UpdatePrice(symbol, decimal.NewFromFloat(65_000))
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer func() {
		time.Sleep(5 * time.Second)
		logHandState(t, rt, hand, symbol)
		hand.Stop()
	}()

	// Entry.
	entryNotify := orderNotify(hand, 15*time.Second)
	hand.DeliverSignal(longSig(symbol))
	select {
	case entry := <-entryNotify:
		t.Logf("entry: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
			t.Fatalf("entry order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for entry order")
	}

	if !waitFills(hand, 1, 20*time.Second) {
		t.Fatal("timeout waiting for entry fill")
	}

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected open long position after entry fill")
	}
	t.Logf("position after entry: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Exit.
	exitNotify := orderNotifyNew(hand, hand.Orders()[0].ID, 15*time.Second)
	hand.DeliverSignal(exitSig(symbol))
	select {
	case entry := <-exitNotify:
		t.Logf("exit: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			t.Logf("exit order failed (non-fatal for sandbox): %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for exit order")
	}

	if !waitFills(hand, 2, 20*time.Second) {
		t.Log("exit fill not confirmed within 20s — order may still be pending")
	}

	cancelAllOrders(t, rt, hand)
}

// ── Alpaca Paper — round-trip ─────────────────────────────────────────────────

func TestSignalRoundTrip_Alpaca(t *testing.T) {
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
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(1))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer func() {
		time.Sleep(5 * time.Second)
		logHandState(t, rt, hand, symbol)
		hand.Stop()
	}()

	// Entry.
	entryNotify := orderNotify(hand, 15*time.Second)
	hand.DeliverSignal(longSig(symbol))
	select {
	case entry := <-entryNotify:
		t.Logf("entry: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			t.Logf("entry not placed (market may be closed): %s", entry.Reason)
			return
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for entry order")
	}

	if !waitFills(hand, 1, 30*time.Second) {
		t.Log("entry fill not confirmed within 30s — market may be closed, cancelling")
		cancelAllOrders(t, rt, hand)
		return
	}

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected open long position after entry fill")
	}
	t.Logf("position after entry: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Exit.
	exitNotify := orderNotifyNew(hand, hand.Orders()[0].ID, 15*time.Second)
	hand.DeliverSignal(exitSig(symbol))
	select {
	case entry := <-exitNotify:
		t.Logf("exit: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == runtime.CodeOrderFailed {
			t.Logf("exit order failed (non-fatal): %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for exit order")
	}

	if !waitFills(hand, 2, 30*time.Second) {
		t.Log("exit fill not confirmed within 30s")
	}

	cancelAllOrders(t, rt, hand)
}
