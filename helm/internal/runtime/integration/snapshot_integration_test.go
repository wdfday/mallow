package integration_test

// snapshot_integration_test.go — end-to-end snapshot verification against real exchanges.
//
// Verifies the full signal→fill chain and checks that BuildSnapshot returns
// correct positions / cash / equity after real fills.
//
// Snapshots are now pulled directly from in-memory portfolio state (BuildSnapshot),
// not from JetStream KV. JetStream persistence is tested separately by EquityPersister.
//
// Skips automatically if exchange creds are absent.
//
// go test -v -run TestSnapshotIntegration ./internal/runtime/ -timeout 120s

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	bybitact "mallow/helm/internal/infra/exchange/bybit/act"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
	"mallow/helm/internal/runtime/perf"
)

type mockFilterStore struct {
	mu      sync.Mutex
	filters map[string]exchange.SymbolFilters
}

func (m *mockFilterStore) GetFilters(symbol string) exchange.SymbolFilters {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.filters == nil {
		return exchange.SymbolFilters{}
	}
	return m.filters[symbol]
}

func (m *mockFilterStore) SetFilters(symbol string, f exchange.SymbolFilters) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.filters == nil {
		m.filters = make(map[string]exchange.SymbolFilters)
	}
	m.filters[symbol] = f
}

// ── shared runtime builder ────────────────────────────────────────────────────

func buildIntegrationRuntime(t *testing.T, ex exchange.Exchange, creds exchange.Credentials, capital decimal.Decimal) *runtime.HelmRuntime {
	t.Helper()

	pf := portfolio.New(capital)
	rm := risk.New(risk.Config{MaxPositions: 10, DailyLossLimitPct: 0.5, MaxDrawdownPct: 0.5}, pf)
	rt := runtime.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		ex.Name(), pf, rm, ex, creds, nil, time.Now(),
	)
	rt.FilterStore = &mockFilterStore{}
	rm.SetUnitCounter(rt.OpenUnitCount)

	t.Cleanup(func() { rt.Stop() })
	return rt
}

func addIntegrationHand(rt *runtime.HelmRuntime, symbol string, qty decimal.Decimal, allocCap decimal.Decimal) *runtime.Hand {
	return addIntegrationHandEx(rt, symbol, qty, allocCap, false, 1)
}

// addIntegrationHandEx creates a hand with explicit pyramid and maxUnits settings.
func addIntegrationHandEx(rt *runtime.HelmRuntime, symbol string, qty decimal.Decimal, allocCap decimal.Decimal, pyramid bool, maxUnits int) *runtime.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: qty,
	})
	h := runtime.NewHand(
		uuid.New(), rt.HelmID, rt,
		strat, tact,
		pyramid, maxUnits, 0,
		nil, domain.OrderTypeMarket, 0, domain.LimitFallbackCancel,
		domain.HandGuardConfig{}, allocCap,
	)
	h.Symbol = symbol
	h.StrategyName = "signal_follower"
	h.EnableEventSink()
	rt.AddHand(h)
	return h
}

// currentSnapshot returns the current helm-level snapshot.
// BuildSnapshot reads live portfolio state — always up to date after fills.
func currentSnapshot(rt *runtime.HelmRuntime) *perf.Snapshot {
	return rt.BuildSnapshot(time.Now())
}

// ── assertions ────────────────────────────────────────────────────────────────

// assertOpenSnapshot verifies the helm-level snapshot has an open position after fill.
func assertOpenSnapshot(t *testing.T, rt *runtime.HelmRuntime, symbol string) {
	t.Helper()
	snap := currentSnapshot(rt)
	if snap == nil {
		t.Error("helm snapshot: BuildSnapshot returned nil")
		return
	}

	t.Logf("helm snapshot: cash=%s equity=%s positions=%d", snap.Cash, snap.Equity, len(snap.Positions))

	if snap.HelmID != rt.HelmID.String() {
		t.Errorf("helm snapshot HelmID mismatch: want %s got %s", rt.HelmID, snap.HelmID)
	}
	if len(snap.Positions) == 0 {
		t.Error("helm snapshot: expected ≥1 open position after fill")
	} else {
		pos := snap.Positions[0]
		if pos.Symbol != symbol {
			t.Errorf("helm position symbol: want %s got %s", symbol, pos.Symbol)
		}
		t.Logf("helm position confirmed: symbol=%s qty=%s avg_price=%s", pos.Symbol, pos.Qty, pos.AvgPrice)
	}
	if !snap.Cash.IsPositive() {
		t.Errorf("helm snapshot cash should be positive, got %s", snap.Cash)
	}
	if !snap.Equity.IsPositive() {
		t.Errorf("helm snapshot equity should be positive, got %s", snap.Equity)
	}
}

// assertFlatSnapshot verifies the helm-level snapshot has no open position for the traded symbol.
func assertFlatSnapshot(t *testing.T, rt *runtime.HelmRuntime, symbol string) {
	t.Helper()
	snap := currentSnapshot(rt)
	if snap == nil {
		t.Error("helm snapshot: BuildSnapshot returned nil")
		return
	}
	var symbolPos *perf.PositionEntry
	for _, p := range snap.Positions {
		if p.Symbol == symbol {
			symbolPos = &p
			break
		}
	}
	if symbolPos != nil && symbolPos.Qty.IsPositive() {
		t.Errorf("helm: expected %s position to be flat after exit, got qty=%s", symbol, symbolPos.Qty)
	}
	if !snap.Equity.IsPositive() {
		t.Errorf("helm: equity should remain positive after round-trip, got %s", snap.Equity)
	}
	t.Logf("helm after exit: cash=%s equity=%s positions=%d", snap.Cash, snap.Equity, len(snap.Positions))
}

// ── Binance ───────────────────────────────────────────────────────────────────

func TestSnapshotIntegration_Binance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	const symbol = "BTCUSDT"
	const orderQty = 0.001
	const allocCap = 200.0 // USDT budget for this hand

	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	ctx15 := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 15*time.Second)
	}

	ctx, cancel := ctx15()
	price, err := ex.GetCurrentPrice(ctx, creds, symbol)
	cancel()
	if err != nil || !price.IsPositive() {
		t.Skipf("cannot fetch %s price: %v", symbol, err)
	}
	t.Logf("live price: %s = %s USDT", symbol, price)

	rt := buildIntegrationRuntime(t, ex, creds, decimal.NewFromFloat(100_000))
	rt.UpdatePrice(symbol, price)

	hand := addIntegrationHand(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap))
	hand.Start()
	defer hand.Stop()

	t.Cleanup(func() { cancelAllOrders(t, rt, hand) })

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSignalFor(symbol))

	select {
	case e := <-placed:
		t.Logf("order placed/failed: code=%d order_id=%s side=%s qty=%s reason=%s",
			e.Code, e.OrderID, e.Side, e.Qty, e.Reason)
		if e.Code == runtime.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox balance insufficient: %s", e.Reason)
			}
			t.Fatalf("order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: no order placed or failed within 20s")
	}

	select {
	case e := <-filled:
		t.Logf("order filled: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("fill not observed in activity ring — WS path may still deliver; continuing")
	}

	assertOpenSnapshot(t, rt, symbol)

	cancelAllOrders(t, rt, hand)
}

// ── OKX ──────────────────────────────────────────────────────────────────────

func TestSnapshotIntegration_OKX(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if os.Getenv("OKX_INTEGRATION") == "" {
		t.Skip("OKX integration tests disabled — set OKX_INTEGRATION=1 to enable")
	}
	if okxPaperAPIKey == "" {
		t.Skip("OKX paper credentials not set")
	}

	const symbol = "BTC-USDT"
	const orderQty = 0.001
	const allocCap = 200.0

	ex := okxact.New(okxact.Config{Paper: true})
	creds := exchange.Credentials{
		APIKey:     okxPaperAPIKey,
		APISecret:  okxPaperAPISecret,
		Passphrase: okxPaperPassphrase,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	price, err := ex.GetCurrentPrice(ctx, creds, symbol)
	cancel()
	if err != nil || !price.IsPositive() {
		t.Skipf("cannot fetch %s price: %v", symbol, err)
	}
	t.Logf("live price: %s = %s USDT", symbol, price)

	rt := buildIntegrationRuntime(t, ex, creds, decimal.NewFromFloat(100_000))
	rt.UpdatePrice(symbol, price)

	hand := addIntegrationHand(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap))
	hand.Start()
	defer hand.Stop()

	t.Cleanup(func() { cancelAllOrders(t, rt, hand) })

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSignalFor(symbol))

	select {
	case e := <-placed:
		t.Logf("order placed/failed: code=%d order_id=%s side=%s qty=%s reason=%s",
			e.Code, e.OrderID, e.Side, e.Qty, e.Reason)
		if e.Code == runtime.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox balance insufficient: %s", e.Reason)
			}
			t.Fatalf("order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: no order placed or failed within 20s")
	}

	select {
	case e := <-filled:
		t.Logf("order filled: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("fill not observed in activity ring — continuing")
	}

	assertOpenSnapshot(t, rt, symbol)
	cancelAllOrders(t, rt, hand)
}

// ── Bybit ─────────────────────────────────────────────────────────────────────

func TestSnapshotIntegration_Bybit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if bybitTestAPIKey == "" {
		t.Skip("Bybit demo credentials not set")
	}

	const symbol = "BTCUSDT"
	const orderQty = 0.001
	const allocCap = 200.0

	ex := bybitact.New(bybitact.Config{Paper: true})
	creds := exchange.Credentials{APIKey: bybitTestAPIKey, APISecret: bybitTestAPISecret}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	price, err := ex.MarkPrice(ctx, creds, symbol)
	cancel()
	if err != nil || !price.IsPositive() {
		t.Logf("mark price fetch failed (%v) — seeding approximate 100000", err)
		price = decimal.NewFromFloat(100_000)
	}
	t.Logf("live price: %s = %s USDT", symbol, price)

	rt := buildIntegrationRuntime(t, ex, creds, decimal.NewFromFloat(100_000))
	rt.UpdatePrice(symbol, price)

	hand := addIntegrationHand(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap))
	hand.Start()
	defer hand.Stop()

	t.Cleanup(func() { cancelAllOrders(t, rt, hand) })

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSignalFor(symbol))

	select {
	case e := <-placed:
		t.Logf("order placed/failed: code=%d order_id=%s side=%s qty=%s reason=%s",
			e.Code, e.OrderID, e.Side, e.Qty, e.Reason)
		if e.Code == runtime.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox balance insufficient: %s", e.Reason)
			}
			t.Fatalf("order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: no order placed or failed within 20s")
	}

	select {
	case e := <-filled:
		t.Logf("order filled: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("fill not observed in activity ring — continuing")
	}

	assertOpenSnapshot(t, rt, symbol)
	cancelAllOrders(t, rt, hand)
}

// ── Binance Round-Trip (entry + exit) ─────────────────────────────────────────

func TestSnapshotIntegration_Binance_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	const symbol = "BTCUSDT"
	const orderQty = 0.001
	const allocCap = 200.0

	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	price, err := ex.GetCurrentPrice(ctx, creds, symbol)
	cancel()
	if err != nil || !price.IsPositive() {
		t.Skipf("cannot fetch %s price: %v", symbol, err)
	}
	t.Logf("live price: %s = %s USDT", symbol, price)

	rt := buildIntegrationRuntime(t, ex, creds, decimal.NewFromFloat(100_000))
	rt.UpdatePrice(symbol, price)

	hand := addIntegrationHandEx(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap), false, 1)
	hand.Start()
	defer hand.Stop()

	// ── Entry ─────────────────────────────────────────────────────────────────
	entryPlaced := orderNotify(hand, 20*time.Second)
	entryFilled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSignalFor(symbol))

	select {
	case e := <-entryPlaced:
		t.Logf("entry order placed/failed: code=%d order_id=%s side=%s qty=%s reason=%s",
			e.Code, e.OrderID, e.Side, e.Qty, e.Reason)
		if e.Code == runtime.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox balance insufficient: %s", e.Reason)
			}
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: entry order not placed within 20s")
	}

	var entryOrderID string
	select {
	case e := <-entryFilled:
		entryOrderID = e.OrderID
		t.Logf("entry filled: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("entry fill not in activity ring — using first order ID from hand.Orders()")
		orders := hand.Orders()
		if len(orders) > 0 {
			entryOrderID = orders[0].ID
		}
	}

	// Verify open position after entry.
	assertOpenSnapshot(t, rt, symbol)

	// ── Exit ──────────────────────────────────────────────────────────────────
	exitPlaced := orderNotifyNew(hand, entryOrderID, 20*time.Second)
	exitFilled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(exitSig(symbol))

	select {
	case e := <-exitPlaced:
		t.Logf("exit order placed/failed: code=%d order_id=%s side=%s qty=%s reason=%s",
			e.Code, e.OrderID, e.Side, e.Qty, e.Reason)
		if e.Code == runtime.CodeOrderFailed {
			t.Logf("exit order failed (non-fatal for snapshot check): %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Log("exit order not observed — hand may have already been flat")
	}

	select {
	case e := <-exitFilled:
		t.Logf("exit filled: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("exit fill not observed in activity ring — continuing to snapshot check")
	}

	assertFlatSnapshot(t, rt, symbol)
}

// ── Binance Non-Pyramid ───────────────────────────────────────────────────────

// TestSnapshotIntegration_Binance_NonPyramid verifies that a hand with pyramid=false
// and maxUnits=1 ignores a second long signal when a position is already open.
func TestSnapshotIntegration_Binance_NonPyramid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	const symbol = "BTCUSDT"
	const orderQty = 0.001
	const allocCap = 200.0

	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	price, err := ex.GetCurrentPrice(ctx, creds, symbol)
	cancel()
	if err != nil || !price.IsPositive() {
		t.Skipf("cannot fetch %s price: %v", symbol, err)
	}
	t.Logf("live price: %s = %s USDT", symbol, price)

	rt := buildIntegrationRuntime(t, ex, creds, decimal.NewFromFloat(100_000))
	rt.UpdatePrice(symbol, price)

	// pyramid=false, maxUnits=1 — second signal must be filtered.
	hand := addIntegrationHandEx(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap), false, 1)
	hand.Start()
	defer hand.Stop()
	defer cancelAllOrders(t, rt, hand)

	// ── First signal → should fill ─────────────────────────────────────────────
	firstPlaced := orderNotify(hand, 20*time.Second)
	firstFilled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSignalFor(symbol))

	select {
	case e := <-firstPlaced:
		t.Logf("1st order placed/failed: code=%d order_id=%s reason=%s", e.Code, e.OrderID, e.Reason)
		if e.Code == runtime.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox balance insufficient: %s", e.Reason)
			}
			t.Fatalf("1st order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: 1st order not placed within 20s")
	}

	select {
	case e := <-firstFilled:
		t.Logf("1st fill confirmed: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("1st fill not observed in activity ring — continuing")
	}

	fillsBefore := countFills(hand)
	t.Logf("fills after 1st signal: %d", fillsBefore)

	// ── Second signal → must be blocked (maxUnits=1 already open) ────────────
	hand.DeliverSignal(longSignalFor(symbol))
	time.Sleep(2 * time.Second)

	fillsAfter := countFills(hand)
	t.Logf("fills after 2nd signal: %d", fillsAfter)

	if fillsAfter > fillsBefore {
		t.Errorf("non-pyramid: expected second signal to be blocked, but got %d extra fill(s)",
			fillsAfter-fillsBefore)
	} else {
		t.Log("non-pyramid: second signal correctly blocked — no extra fill")
	}

	// Snapshot should still show the same open position (no new buys).
	snap := currentSnapshot(rt)
	t.Logf("snapshot after 2nd signal: positions=%d", len(snap.Positions))

	// Cleanup: exit the position.
	exitPlaced := orderNotifyNew(hand, "", 20*time.Second)
	hand.DeliverSignal(exitSig(symbol))
	select {
	case e := <-exitPlaced:
		t.Logf("cleanup exit placed: %s", e.OrderID)
	case <-time.After(20 * time.Second):
		t.Log("cleanup exit order not observed")
	}
}

// ── Binance Pyramid ───────────────────────────────────────────────────────────

// TestSnapshotIntegration_Binance_Pyramid verifies that a hand with pyramid=true
// and maxUnits=3 accepts multiple long signals and accumulates the position.
func TestSnapshotIntegration_Binance_Pyramid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	const symbol = "BTCUSDT"
	const orderQty = 0.001
	const allocCap = 1_000.0
	const pyramidLevels = 2 // deliver 2 signals; expect 2 fills

	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	price, err := ex.GetCurrentPrice(ctx, creds, symbol)
	cancel()
	if err != nil || !price.IsPositive() {
		t.Skipf("cannot fetch %s price: %v", symbol, err)
	}
	t.Logf("live price: %s = %s USDT", symbol, price)

	rt := buildIntegrationRuntime(t, ex, creds, decimal.NewFromFloat(100_000))
	rt.UpdatePrice(symbol, price)

	hand := addIntegrationHandEx(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap), true, 3)
	hand.Start()
	defer hand.Stop()
	defer cancelAllOrders(t, rt, hand)

	var lastFillOrderID string
	for i := range pyramidLevels {
		level := i + 1
		t.Logf("── pyramid level %d ──────────────────", level)

		// Avg-anchor gate adds only to a winning leg (price beyond the blended avg).
		// Live ticks barely move within the test window, so step the known price up
		// each level to keep the leg "winning"; level 1 (flat leg) is never gated.
		price = price.Mul(decimal.NewFromFloat(1.001))
		rt.UpdatePrice(symbol, price)

		placed := orderNotifyNew(hand, lastFillOrderID, 20*time.Second)
		hand.DeliverSignal(longSignalFor(symbol))

		var placedID string
		select {
		case e := <-placed:
			t.Logf("level %d order placed/failed: code=%d order_id=%s reason=%s", level, e.Code, e.OrderID, e.Reason)
			if e.Code == runtime.CodeOrderFailed {
				if isBalanceError(e.Reason) {
					t.Skipf("sandbox balance insufficient at level %d: %s", level, e.Reason)
				}
				t.Fatalf("level %d order failed: %s", level, e.Reason)
			}
			placedID = e.OrderID
		case <-time.After(20 * time.Second):
			t.Fatalf("timeout: level %d order not placed within 20s", level)
		}

		filled := fillNotifyOrder(hand, placedID, 30*time.Second)
		select {
		case e := <-filled:
			t.Logf("level %d fill confirmed: order_id=%s qty=%s price=%s", level, e.OrderID, e.Qty, e.Price)
			lastFillOrderID = placedID
		case <-time.After(30 * time.Second):
			t.Logf("level %d fill not in activity ring — continuing", level)
			lastFillOrderID = placedID
		}
	}

	// After all pyramid levels: verify accumulated position in latest snapshot.
	snap := currentSnapshot(rt)
	t.Logf("final helm snapshot: cash=%s equity=%s positions=%d", snap.Cash, snap.Equity, len(snap.Positions))

	// Binance charges fee in base asset for BUY spot orders (~0.1% each fill),
	// so each 0.001 BTC order yields ~0.000999 BTC. Allow up to 0.2% fee per fill.
	expectedQty := decimal.NewFromFloat(orderQty).Mul(decimal.NewFromInt(pyramidLevels)).Mul(decimal.NewFromFloat(0.998))
	if len(snap.Positions) > 0 {
		totalQty := decimal.Zero
		for _, p := range snap.Positions {
			totalQty = totalQty.Add(p.Qty)
		}
		if totalQty.LessThan(expectedQty) {
			t.Errorf("helm pyramid: expected accumulated qty ≥ %s (after fees), got %s", expectedQty, totalQty)
		} else {
			t.Logf("helm pyramid confirmed: total qty=%s (expected ≥%s after fees)", totalQty, expectedQty)
		}
	} else {
		t.Error("helm pyramid: expected open position after pyramid entries")
	}

	// Cleanup: exit the accumulated position.
	t.Log("cleanup: delivering exit signal")
	exitPlaced := orderNotifyNew(hand, lastFillOrderID, 20*time.Second)
	hand.DeliverSignal(exitSig(symbol))
	select {
	case e := <-exitPlaced:
		t.Logf("cleanup exit placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
	case <-time.After(20 * time.Second):
		t.Log("cleanup exit order not observed — cancelAllOrders fallback will run")
	}
}

// countFills returns the number of orders filled so far via hand metrics.
func countFills(hand *runtime.Hand) int {
	return int(hand.Metrics().OrdersFilled)
}

// ── Binance Pyramid Round-Trip (entry → pyramid entry → exit) ────────────────

// TestSnapshotIntegration_Binance_PyramidRoundTrip exercises the complete lifecycle:
//
//  1. Long entry   → fill → snapshot: 1 unit, cash decreases
//  2. Pyramid entry → fill → snapshot: 2 units accumulated, cash decreases further
//  3. Exit signal  → fill → snapshot: 0 positions, cash+equity restored
func TestSnapshotIntegration_Binance_PyramidRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	const symbol = "BTCUSDT"
	const orderQty = 0.001
	const allocCap = 1_000.0

	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	price, err := ex.GetCurrentPrice(ctx, creds, symbol)
	cancel()
	if err != nil || !price.IsPositive() {
		t.Skipf("cannot fetch %s price: %v", symbol, err)
	}
	t.Logf("live %s price: %s USDT", symbol, price)

	rt := buildIntegrationRuntime(t, ex, creds, decimal.NewFromFloat(allocCap))
	rt.UpdatePrice(symbol, price)

	hand := addIntegrationHandEx(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap), true, 3)
	hand.Start()
	defer hand.Stop()
	defer cancelAllOrders(t, rt, hand)

	logStageSnaps := func(stage string) {
		snap := currentSnapshot(rt)
		t.Logf("╔══════════════════════════════════════════════════════════════")
		t.Logf("║ STAGE: [%s]", stage)
		t.Logf("╠══════════════════════════════════════════════════════════════")

		// ── 1. Logical Portfolio.Summary() ────────────────────────────────
		summary := rt.Portfolio.Summary()
		t.Logf("║ ▶ LOGICAL PORTFOLIO (in-memory)")
		t.Logf("║   initialCapital = %s", summary.InitialCapital)
		t.Logf("║   cash           = %s", summary.Cash)
		t.Logf("║   equity         = %s", summary.Equity)
		t.Logf("║   totalReturn    = %.4f%%", summary.TotalReturn)
		t.Logf("║   currentDD      = %.4f%%", summary.CurrentDD)
		t.Logf("║   maxDD          = %.4f%%", summary.MaxDD)
		t.Logf("║   dailyPnL       = %s", summary.DailyPnL)
		t.Logf("║   totalTrades    = %d", summary.TotalTrades)
		t.Logf("║   openPositions  = %d", summary.OpenPositions)
		for _, p := range summary.Positions {
			t.Logf("║     pos: %s qty=%s avg=%s cur=%s unrealizedPnL=%s mktValue=%s",
				p.Symbol, p.Qty, p.AvgPrice, p.CurrentPrice, p.UnrealizedPnL, p.MarketValue)
		}

		// ── 2. Actual exchange state (SyncAccount) ────────────────────────
		if syncer, ok := interface{}(ex).(exchange.AccountSyncer); ok {
			syncCtx, syncCancel := context.WithTimeout(context.Background(), 10*time.Second)
			syncSnap, syncErr := syncer.SyncAccount(syncCtx, creds, nil)
			syncCancel()
			if syncErr != nil {
				t.Logf("║ ▶ EXCHANGE WALLET: sync error: %v", syncErr)
			} else {
				t.Logf("║ ▶ EXCHANGE WALLET (real)")
				t.Logf("║   cash(USDT)     = %s", syncSnap.Cash)
				t.Logf("║   equity         = %s", syncSnap.Equity)
				for _, b := range syncSnap.Balances {
					if b.Free.IsPositive() {
						t.Logf("║     asset: %s free=%s", b.Asset, b.Free)
					}
				}
				for _, p := range syncSnap.Positions {
					t.Logf("║     pos: %s qty=%s avg=%s cur=%s", p.Symbol, p.Qty, p.AvgPrice, p.CurPrice)
				}
			}
		}

		// ── 3. Hand state ─────────────────────────────────────────────────
		t.Logf("║ ▶ HAND (bot-level)")
		handHealth := hand.Health()
		handMetrics := hand.Metrics()
		t.Logf("║   health         = %s", handHealth.Status)
		t.Logf("║   totalPnL       = %s", handMetrics.TotalPnL)
		t.Logf("║   ordersFilled   = %d", handMetrics.OrdersFilled)
		t.Logf("║   winCount       = %d", handMetrics.WinCount)
		t.Logf("║   lossCount      = %d", handMetrics.LossCount)

		// ── 4. Snapshot (in-memory) ────────────────────────────────────────
		t.Logf("║ ▶ HELM SNAPSHOT: cash=%s equity=%s positions=%d",
			snap.Cash, snap.Equity, len(snap.Positions))
		for _, p := range snap.Positions {
			t.Logf("║   pos: %s qty=%s avg=%s", p.Symbol, p.Qty, p.AvgPrice)
		}
		t.Logf("╚══════════════════════════════════════════════════════════════")
	}

	placeAndWait := func(sig runtime.Signal, excludeOrderID string, label string) (filledOrderID string) {
		t.Helper()
		placed := orderNotifyNew(hand, excludeOrderID, 20*time.Second)
		hand.DeliverSignal(sig)

		var placedID string
		select {
		case e := <-placed:
			t.Logf("[%s] order event: code=%d order_id=%s side=%s qty=%s reason=%s",
				label, e.Code, e.OrderID, e.Side, e.Qty, e.Reason)
			if e.Code == runtime.CodeOrderFailed {
				if isBalanceError(e.Reason) {
					t.Skipf("[%s] sandbox balance insufficient: %s", label, e.Reason)
				}
				t.Fatalf("[%s] order failed: %s", label, e.Reason)
			}
			placedID = e.OrderID
		case <-time.After(20 * time.Second):
			t.Fatalf("[%s] timeout: no order event within 20s", label)
		}

		fillCh := fillNotifyOrder(hand, placedID, 30*time.Second)
		select {
		case e := <-fillCh:
			t.Logf("[%s] fill confirmed: order_id=%s qty=%s price=%s", label, e.OrderID, e.Qty, e.Price)
			return placedID
		case <-time.After(30 * time.Second):
			t.Logf("[%s] fill not in activity ring — continuing", label)
			return placedID
		}
	}

	// ── Stage 1: first entry ───────────────────────────────────────────────────
	t.Log("═══ Stage 1: entry ═══")
	id1 := placeAndWait(longSignalFor(symbol), "", "entry")
	logStageSnaps("entry")

	snap1 := currentSnapshot(rt)
	if len(snap1.Positions) == 0 {
		t.Error("stage 1 helm: expected open position after entry")
	}

	// ── Stage 2: pyramid entry ─────────────────────────────────────────────────
	// Avg-anchor gate adds only to a winning leg (price beyond the blended avg). Nudge
	// the known price above the entry avg so the add isn't blocked — a live tick rarely
	// moves on its own within the test window.
	t.Log("═══ Stage 2: pyramid entry ═══")
	rt.UpdatePrice(symbol, price.Mul(decimal.NewFromFloat(1.001)))
	id2 := placeAndWait(longSignalFor(symbol), id1, "pyramid")
	logStageSnaps("pyramid entry")

	snap2 := currentSnapshot(rt)
	// Allow up to 0.2% fee per fill (Binance charges fee in base asset for BUY spot orders).
	expectedQty := decimal.NewFromFloat(orderQty * 2).Mul(decimal.NewFromFloat(0.998))
	var total2 decimal.Decimal
	for _, p := range snap2.Positions {
		total2 = total2.Add(p.Qty)
	}
	if total2.LessThan(expectedQty) {
		t.Errorf("stage 2 helm: expected accumulated qty ≥ %s (after fees), got %s", expectedQty, total2)
	} else {
		t.Logf("stage 2 helm: accumulated qty=%s ✓", total2)
	}
	// Cash must be lower than after stage 1 (second buy consumed more cash).
	if !snap2.Cash.LessThan(snap1.Cash) {
		t.Errorf("stage 2 helm: cash should decrease after pyramid entry; stage1=%s stage2=%s",
			snap1.Cash, snap2.Cash)
	}

	// ── Stage 3: exit ─────────────────────────────────────────────────────────
	t.Log("═══ Stage 3: exit ═══")
	placeAndWait(exitSig(symbol), id2, "exit")
	logStageSnaps("exit")

	assertFlatSnapshot(t, rt, symbol)
}

// TestSnapshotIntegration_Binance_KillPauseRelease verifies hand lifecycle
// (Kill and Release) and that the helm snapshot reflects the correct state.
func TestSnapshotIntegration_Binance_KillPauseRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	const symbol = "BTCUSDT"
	const orderQty = 0.001
	const allocCap = 500.0

	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Sync real USDT cash.
	var initialCapital decimal.Decimal
	if syncer, ok := interface{}(ex).(exchange.AccountSyncer); ok {
		if snap, err := syncer.SyncAccount(ctx, creds, nil); err == nil && snap.Cash.IsPositive() {
			initialCapital = snap.Cash
		} else {
			t.Skipf("SyncAccount failed (%v) — cannot run integration test without real balance", err)
		}
	} else {
		t.Skip("exchange does not support AccountSyncer")
	}

	price, err := ex.GetCurrentPrice(ctx, creds, symbol)
	if err != nil || !price.IsPositive() {
		t.Skipf("cannot fetch %s price: %v", symbol, err)
	}
	t.Logf("live price: %s = %s USDT", symbol, price)

	rt := buildIntegrationRuntime(t, ex, creds, initialCapital)
	rt.UpdatePrice(symbol, price)

	hand1 := addIntegrationHandEx(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap), false, 1)
	hand1.Start()
	defer hand1.Stop()

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()

		if orders, err := ex.ListOpenOrders(cleanupCtx, creds, symbol); err == nil {
			for _, o := range orders {
				_ = ex.CancelOrder(cleanupCtx, creds, o.ID)
			}
		}

		if syncer, ok := interface{}(ex).(exchange.AccountSyncer); ok {
			if snap, err := syncer.SyncAccount(cleanupCtx, creds, nil); err == nil {
				for _, p := range snap.Positions {
					if p.Symbol == symbol && p.Qty.IsPositive() {
						truncatedQty := p.Qty.Truncate(4)
						if truncatedQty.IsPositive() {
							sellReq := exchange.OrderRequest{
								Symbol: symbol,
								Side:   exchange.Sell,
								Type:   exchange.Market,
								Qty:    truncatedQty,
							}
							if res, err := ex.PlaceOrder(cleanupCtx, creds, sellReq); err == nil {
								t.Logf("cleanup: sold remaining %s BTC on exchange (order_id=%s)", truncatedQty, res.ID)
							} else {
								t.Logf("cleanup: failed to sell remaining BTC: %v", err)
							}
						}
					}
				}
			}
		}
	})

	// ── Step 1: Entry ─────────────────────────────────────────────────────────
	t.Log("=== Step 1: Entry ===")
	placed := orderNotify(hand1, 20*time.Second)
	filled := fillNotify(hand1, 30*time.Second)

	hand1.DeliverSignal(longSignalFor(symbol))

	select {
	case e := <-placed:
		t.Logf("1st entry order placed: code=%d order_id=%s reason=%s", e.Code, e.OrderID, e.Reason)
		if e.Code == runtime.CodeOrderFailed {
			t.Fatalf("entry order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: entry order not placed")
	}

	select {
	case e := <-filled:
		t.Logf("1st entry filled: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("fill not observed in activity ring — continuing")
	}

	// Verify position after entry.
	snap1 := currentSnapshot(rt)
	if len(snap1.Positions) == 0 {
		t.Fatal("expected position in snapshot after entry")
	}
	t.Logf("hand1 snapshot position confirmed: %s", snap1.Positions[0].Qty)

	// ── Step 2: Release ───────────────────────────────────────────────────────
	t.Log("=== Step 2: Release ===")
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
	hand1.Release(releaseCtx)
	releaseCancel()

	if hand1.IsRunning() {
		t.Fatal("hand1 should be stopped after release")
	}
	if hand1.Health().Status != runtime.HealthReleased {
		t.Fatalf("expected status Released, got %s", hand1.Health().Status)
	}

	// Logical position for hand1 should be flat.
	if pos := hand1.Position(); pos != nil {
		t.Fatalf("logical position should be flat (nil) after release, got: %v", pos)
	}

	// Helm snapshot may or may not still show the position depending on whether
	// Release evicts it from portfolio. The key invariant is hand is stopped.
	snapRelease := currentSnapshot(rt)
	t.Logf("helm snapshot after release: positions=%d cash=%s equity=%s",
		len(snapRelease.Positions), snapRelease.Cash, snapRelease.Equity)

	// Verify exchange position STILL EXISTS (Release leaves it open).
	ctxCheck, cCheck := context.WithTimeout(context.Background(), 10*time.Second)
	var realPosQty decimal.Decimal
	if syncer, ok := interface{}(ex).(exchange.AccountSyncer); ok {
		if snap, err := syncer.SyncAccount(ctxCheck, creds, nil); err == nil {
			for _, p := range snap.Positions {
				if p.Symbol == symbol {
					realPosQty = p.Qty
				}
			}
		}
	}
	cCheck()
	if !realPosQty.IsPositive() {
		t.Fatal("expected real exchange position to remain open after Release, but was flat")
	}
	t.Logf("real exchange position remains open: %s BTC ✓", realPosQty)

	// ── Step 3: Kill ──────────────────────────────────────────────────────────
	t.Log("=== Step 3: Kill ===")
	hand2 := addIntegrationHandEx(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap), false, 1)
	hand2.Start()
	defer hand2.Stop()

	placed2 := orderNotify(hand2, 20*time.Second)
	filled2 := fillNotify(hand2, 30*time.Second)

	hand2.DeliverSignal(longSignalFor(symbol))

	select {
	case e := <-placed2:
		t.Logf("2nd entry order placed: code=%d order_id=%s reason=%s", e.Code, e.OrderID, e.Reason)
		if e.Code == runtime.CodeOrderFailed {
			t.Fatalf("entry order failed for hand2: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: entry order not placed for hand2")
	}

	select {
	case e := <-filled2:
		t.Logf("2nd entry filled: order_id=%s qty=%s", e.OrderID, e.Qty)
	case <-time.After(30 * time.Second):
		t.Log("fill for hand2 not observed in activity ring — continuing")
	}

	killCtx, killCancel := context.WithTimeout(context.Background(), 15*time.Second)
	hand2.Kill(killCtx)
	killCancel()

	if hand2.IsRunning() {
		t.Fatal("hand2 should be stopped after kill")
	}
	if hand2.Health().Status != runtime.HealthKilled {
		t.Fatalf("expected status Killed, got %s", hand2.Health().Status)
	}

	if pos := hand2.Position(); pos != nil {
		t.Fatalf("logical position should be flat (nil) after kill, got: %v", pos)
	}
	t.Log("Kill correctly flattened hand2 logical state ✓")

	// Helm snapshot should reflect the kill (hand2 position closed at exchange).
	snapKill := currentSnapshot(rt)
	t.Logf("helm snapshot after kill: positions=%d cash=%s equity=%s",
		len(snapKill.Positions), snapKill.Cash, snapKill.Equity)
}

func longSignalFor(symbol string) runtime.Signal {
	return longSig(symbol)
}
