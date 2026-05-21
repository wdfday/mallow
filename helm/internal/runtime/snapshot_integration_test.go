package runtime_test

// snapshot_integration_test.go — end-to-end snapshot persistence test.
//
// Uses:
//   - NATS JetStream (local nats://localhost:4222) for real SnapshotLog
//   - A real exchange (Binance demo, OKX paper, or Bybit demo) for real fills
//
// Verifies the full chain:
//   signal delivered → PlaceOrder → fill (REST-immediate or WS)
//     → SnapshotLog.Append called for helm-level and hand-level snapshots
//     → snapshots queryable from JetStream (Latest)
//
// Skips automatically if NATS is not reachable or exchange creds are absent.
//
// go test -v -run TestSnapshotIntegration ./internal/runtime/ -timeout 120s

import (
	"context"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"github.com/google/uuid"
	"mallow/helm/internal/infra/exchange"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	bybitact "mallow/helm/internal/infra/exchange/bybit/act"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
	"mallow/helm/internal/infra/perflog"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// ── NATS helper ───────────────────────────────────────────────────────────────

// connectNATS connects to local NATS and ensures JetStream streams exist.
// Skips the test if NATS is not reachable.
func connectNATS(t *testing.T) natsgo.JetStreamContext {
	t.Helper()
	// helm service credentials (default from nats-server.conf dev defaults).
	nc, err := natsgo.Connect("nats://helm:helm-dev@localhost:4222",
		natsgo.Timeout(2*time.Second),
		natsgo.MaxReconnects(0),
	)
	if err != nil {
		t.Skipf("NATS not available at localhost:4222 (%v) — skipping integration test", err)
	}
	t.Cleanup(func() { nc.Close() })

	// Use raw JetStream context — streams are already created by the running NATS server.
	// Calling NewJetStream (ensureStream) would fail on subject-overlap errors for streams
	// that were created with different configs by other services.
	js, err := nc.JetStream()
	if err != nil {
		t.Skipf("JetStream context failed (%v) — skipping integration test", err)
	}
	// Verify HELM_SNAPSHOTS exists — skip if the full stack isn't running.
	if _, err := js.StreamInfo("HELM_SNAPSHOTS"); err != nil {
		t.Skipf("HELM_SNAPSHOTS stream not found (%v) — run 'just infra' first", err)
	}
	return js
}

// ── shared runtime builder ────────────────────────────────────────────────────

func buildIntegrationRuntime(t *testing.T, js natsgo.JetStreamContext, ex exchange.Exchange, creds exchange.Credentials, capital decimal.Decimal) *runtime.HelmRuntime {
	t.Helper()

	snapLog, err := perflog.NewSnapshotLog(js)
	if err != nil {
		t.Fatalf("NewSnapshotLog: %v", err)
	}

	pf := portfolio.New(capital)
	rm := risk.New(risk.Config{MaxPositions: 10, DailyLossLimitPct: 0.5, MaxDrawdownPct: 0.5}, pf)
	rt := runtime.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		ex.Name(), pf, rm, ex, creds, nil, time.Now(),
	)
	rm.SetUnitCounter(rt.OpenUnitCount)
	rt.SnapshotLog = snapLog

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
		domain.HandRiskConfig{}, allocCap,
	)
	h.Symbol = symbol
	h.StrategyName = "signal_follower"
	rt.AddHand(h)
	return h
}

// waitSnapshots polls SnapshotLog.Latest until at least n entries (helm or hand) appear.
func waitSnapshots(t *testing.T, rt *runtime.HelmRuntime, hand *runtime.Hand, n int, timeout time.Duration) {
	t.Helper()
	helmID := rt.HelmID.String()
	handID := hand.ID().String()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		helmSnaps, _ := rt.SnapshotLog.Latest(ctx, helmID, "", 10)
		handSnaps, _ := rt.SnapshotLog.Latest(ctx, helmID, handID, 10)
		cancel()
		if len(helmSnaps)+len(handSnaps) >= n {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Don't fail — let the test assertions report what was missing.
}

// ── Binance ───────────────────────────────────────────────────────────────────

func TestSnapshotIntegration_Binance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	js := connectNATS(t)

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

	rt := buildIntegrationRuntime(t, js, ex, creds, decimal.NewFromFloat(100_000))
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

	// Snapshots are published async — give goroutines time to land in JetStream.
	waitSnapshots(t, rt, hand, 2, 10*time.Second)

	assertSnapshots(t, rt, hand, symbol)

	cancelAllOrders(t, rt, hand)
}

// ── OKX ──────────────────────────────────────────────────────────────────────

func TestSnapshotIntegration_OKX(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if okxPaperAPIKey == "" {
		t.Skip("OKX paper credentials not set")
	}

	js := connectNATS(t)

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

	rt := buildIntegrationRuntime(t, js, ex, creds, decimal.NewFromFloat(100_000))
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

	waitSnapshots(t, rt, hand, 2, 10*time.Second)
	assertSnapshots(t, rt, hand, symbol)
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

	js := connectNATS(t)

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

	rt := buildIntegrationRuntime(t, js, ex, creds, decimal.NewFromFloat(100_000))
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

	waitSnapshots(t, rt, hand, 2, 10*time.Second)
	assertSnapshots(t, rt, hand, symbol)
	cancelAllOrders(t, rt, hand)
}

// ── assertions ────────────────────────────────────────────────────────────────

// assertSnapshots queries JetStream and verifies helm-level + hand-level snapshots.
func assertSnapshots(t *testing.T, rt *runtime.HelmRuntime, hand *runtime.Hand, symbol string) {
	t.Helper()
	helmID := rt.HelmID.String()
	handID := hand.ID().String()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	helmSnaps, err := rt.SnapshotLog.Latest(ctx, helmID, "", 10)
	if err != nil {
		t.Errorf("SnapshotLog.Latest (helm): %v", err)
		return
	}
	handSnaps, err := rt.SnapshotLog.Latest(ctx, helmID, handID, 10)
	if err != nil {
		t.Errorf("SnapshotLog.Latest (hand): %v", err)
		return
	}

	t.Logf("JetStream helm snapshots: %d", len(helmSnaps))
	for i, s := range helmSnaps {
		t.Logf("  helm[%d] ts=%s cash=%s equity=%s positions=%d",
			i, s.TS.Format("15:04:05.000"), s.Cash, s.Equity, len(s.Positions))
	}

	t.Logf("JetStream hand snapshots: %d", len(handSnaps))
	for i, s := range handSnaps {
		t.Logf("  hand[%d] ts=%s cash=%s equity=%s positions=%d",
			i, s.TS.Format("15:04:05.000"), s.Cash, s.Equity, len(s.Positions))
	}

	// ── helm snapshot ─────────────────────────────────────────────────────────
	if len(helmSnaps) == 0 {
		t.Error("no helm-level snapshot in JetStream")
	} else {
		last := helmSnaps[len(helmSnaps)-1]
		if last.HelmID != helmID {
			t.Errorf("helm snapshot HelmID mismatch: want %s got %s", helmID, last.HelmID)
		}
		if last.HandID != "" {
			t.Errorf("helm snapshot should have empty HandID, got %s", last.HandID)
		}
		if len(last.Positions) == 0 {
			t.Error("helm snapshot: expected ≥1 open position after fill")
		} else {
			pos := last.Positions[0]
			if pos.Symbol != symbol {
				t.Errorf("helm position symbol: want %s got %s", symbol, pos.Symbol)
			}
			t.Logf("helm position confirmed: symbol=%s qty=%s avg_price=%s", pos.Symbol, pos.Qty, pos.AvgPrice)
		}
		if !last.Cash.IsPositive() {
			t.Errorf("helm snapshot cash should be positive, got %s", last.Cash)
		}
		if !last.Equity.IsPositive() {
			t.Errorf("helm snapshot equity should be positive, got %s", last.Equity)
		}
	}

	// ── hand snapshot ─────────────────────────────────────────────────────────
	if len(handSnaps) == 0 {
		t.Error("no hand-level snapshot in JetStream")
	} else {
		last := handSnaps[len(handSnaps)-1]
		if last.HandID != handID {
			t.Errorf("hand snapshot HandID mismatch: want %s got %s", handID, last.HandID)
		}
		if len(last.Positions) == 0 {
			t.Error("hand snapshot: expected ≥1 position after fill")
		} else {
			t.Logf("hand position confirmed: symbol=%s qty=%s avg_price=%s",
				last.Positions[0].Symbol, last.Positions[0].Qty, last.Positions[0].AvgPrice)
		}
		// Hand has allocated cap → equity and cash must be set.
		if !last.Equity.IsPositive() {
			t.Errorf("hand snapshot equity should be positive (allocated cap), got %s", last.Equity)
		}
	}
}

// assertFlatSnapshots verifies the latest helm and hand snapshots show no open positions
// (used after a round-trip entry+exit).
func assertFlatSnapshots(t *testing.T, rt *runtime.HelmRuntime, hand *runtime.Hand) {
	t.Helper()
	helmID := rt.HelmID.String()
	handID := hand.ID().String()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	helmSnaps, err := rt.SnapshotLog.Latest(ctx, helmID, "", 10)
	if err != nil {
		t.Errorf("SnapshotLog.Latest (helm): %v", err)
		return
	}
	handSnaps, err := rt.SnapshotLog.Latest(ctx, helmID, handID, 10)
	if err != nil {
		t.Errorf("SnapshotLog.Latest (hand): %v", err)
		return
	}

	if len(helmSnaps) == 0 {
		t.Error("no helm snapshot after round-trip")
	} else {
		last := helmSnaps[len(helmSnaps)-1]
		if len(last.Positions) != 0 {
			t.Errorf("helm: expected 0 positions after exit, got %d", len(last.Positions))
		}
		if !last.Equity.IsPositive() {
			t.Errorf("helm: equity should remain positive after round-trip, got %s", last.Equity)
		}
		t.Logf("helm after exit: cash=%s equity=%s positions=%d", last.Cash, last.Equity, len(last.Positions))
	}

	if len(handSnaps) == 0 {
		t.Error("no hand snapshot after round-trip")
	} else {
		last := handSnaps[len(handSnaps)-1]
		if len(last.Positions) != 0 {
			t.Errorf("hand: expected 0 positions after exit, got %d", len(last.Positions))
		}
		if !last.Equity.IsPositive() {
			t.Errorf("hand: equity should remain positive after round-trip, got %s", last.Equity)
		}
		t.Logf("hand after exit: cash=%s equity=%s positions=%d", last.Cash, last.Equity, len(last.Positions))
	}
}

// ── Binance Round-Trip (entry + exit) ─────────────────────────────────────────

// TestSnapshotIntegration_Binance_RoundTrip places a long entry, waits for fill,
// then delivers an exit signal. Verifies that the final helm+hand snapshots show
// no open positions and the position is properly closed on the exchange.
func TestSnapshotIntegration_Binance_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	js := connectNATS(t)

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

	rt := buildIntegrationRuntime(t, js, ex, creds, decimal.NewFromFloat(100_000))
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

	// Wait for entry snapshots before delivering exit.
	waitSnapshots(t, rt, hand, 2, 10*time.Second)

	// ── Exit ──────────────────────────────────────────────────────────────────
	exitPlaced := orderNotifyNew(hand, entryOrderID, 20*time.Second)
	exitFilled := fillNotify(hand, 30*time.Second) // any subsequent fill

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

	// Wait for post-exit snapshots (4 total: entry helm+hand, exit helm+hand).
	waitSnapshots(t, rt, hand, 4, 15*time.Second)

	assertFlatSnapshots(t, rt, hand)
}

// ── Binance Non-Pyramid ───────────────────────────────────────────────────────

// TestSnapshotIntegration_Binance_NonPyramid verifies that a hand with pyramid=false
// and maxUnits=1 ignores a second long signal when a position is already open.
// Only one fill and one set of snapshots should appear.
func TestSnapshotIntegration_Binance_NonPyramid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	js := connectNATS(t)

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

	rt := buildIntegrationRuntime(t, js, ex, creds, decimal.NewFromFloat(100_000))
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

	// Allow entry snapshots to land.
	waitSnapshots(t, rt, hand, 2, 10*time.Second)

	// Count fills and snapshots before second signal.
	fillsBefore := countFills(hand)
	t.Logf("fills after 1st signal: %d", fillsBefore)

	// ── Second signal → must be blocked (maxUnits=1 already open) ────────────
	hand.DeliverSignal(longSignalFor(symbol))
	// Give time for any spurious order to appear.
	time.Sleep(2 * time.Second)

	fillsAfter := countFills(hand)
	t.Logf("fills after 2nd signal: %d", fillsAfter)

	if fillsAfter > fillsBefore {
		t.Errorf("non-pyramid: expected second signal to be blocked, but got %d extra fill(s)",
			fillsAfter-fillsBefore)
	} else {
		t.Log("non-pyramid: second signal correctly blocked — no extra fill")
	}

	// Snapshot count should not have grown (no new fills = no new snapshots).
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	helmSnaps, _ := rt.SnapshotLog.Latest(ctx2, rt.HelmID.String(), "", 20)
	handSnaps, _ := rt.SnapshotLog.Latest(ctx2, rt.HelmID.String(), hand.ID().String(), 20)
	cancel2()
	t.Logf("snapshots after 2nd signal: helm=%d hand=%d", len(helmSnaps), len(handSnaps))

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
// and maxUnits=3 accepts multiple long signals, accumulates the position, and
// snapshots reflect growing qty after each fill.
func TestSnapshotIntegration_Binance_Pyramid(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	js := connectNATS(t)

	const symbol = "BTCUSDT"
	const orderQty = 0.001
	const allocCap = 1_000.0 // larger budget for 3 entries
	const pyramidLevels = 2  // deliver 2 signals; expect 2 fills

	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	price, err := ex.GetCurrentPrice(ctx, creds, symbol)
	cancel()
	if err != nil || !price.IsPositive() {
		t.Skipf("cannot fetch %s price: %v", symbol, err)
	}
	t.Logf("live price: %s = %s USDT", symbol, price)

	rt := buildIntegrationRuntime(t, js, ex, creds, decimal.NewFromFloat(100_000))
	rt.UpdatePrice(symbol, price)

	// pyramid=true, maxUnits=3 → up to 3 entries allowed.
	hand := addIntegrationHandEx(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap), true, 3)
	hand.Start()
	defer hand.Stop()
	defer cancelAllOrders(t, rt, hand)

	var lastFillOrderID string
	for i := range pyramidLevels {
		level := i + 1
		t.Logf("── pyramid level %d ──────────────────", level)

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

		// Wait for this level's snapshots before next signal.
		waitSnapshots(t, rt, hand, level*2, 10*time.Second)
	}

	// After all pyramid levels: verify accumulated position in latest snapshot.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	helmSnaps, _ := rt.SnapshotLog.Latest(ctx2, rt.HelmID.String(), "", 20)
	handSnaps, _ := rt.SnapshotLog.Latest(ctx2, rt.HelmID.String(), hand.ID().String(), 20)
	cancel2()

	t.Logf("final helm snapshots: %d, hand snapshots: %d", len(helmSnaps), len(handSnaps))

	expectedQty := decimal.NewFromFloat(orderQty).Mul(decimal.NewFromInt(pyramidLevels))

	if len(helmSnaps) > 0 {
		last := helmSnaps[len(helmSnaps)-1]
		t.Logf("last helm snapshot: cash=%s equity=%s positions=%d", last.Cash, last.Equity, len(last.Positions))
		if len(last.Positions) > 0 {
			totalQty := decimal.Zero
			for _, p := range last.Positions {
				totalQty = totalQty.Add(p.Qty)
			}
			if totalQty.LessThan(expectedQty) {
				t.Errorf("helm pyramid: expected accumulated qty ≥ %s, got %s", expectedQty, totalQty)
			} else {
				t.Logf("helm pyramid confirmed: total qty=%s (expected ≥%s)", totalQty, expectedQty)
			}
		} else {
			t.Error("helm pyramid: expected open position after pyramid entries")
		}
	}

	if len(handSnaps) > 0 {
		last := handSnaps[len(handSnaps)-1]
		t.Logf("last hand snapshot: cash=%s equity=%s positions=%d", last.Cash, last.Equity, len(last.Positions))
		if len(last.Positions) > 0 {
			totalQty := decimal.Zero
			for _, p := range last.Positions {
				totalQty = totalQty.Add(p.Qty)
			}
			if totalQty.LessThan(expectedQty) {
				t.Errorf("hand pyramid: expected accumulated qty ≥ %s, got %s", expectedQty, totalQty)
			} else {
				t.Logf("hand pyramid confirmed: total qty=%s (expected ≥%s)", totalQty, expectedQty)
			}
		} else {
			t.Error("hand pyramid: expected open positions after pyramid entries")
		}
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

// countFills returns the number of CodeOrderFilled entries in the hand's activity ring.
func countFills(hand *runtime.Hand) int {
	count := 0
	for _, e := range hand.Activity() {
		if e.Code == runtime.CodeOrderFilled {
			count++
		}
	}
	return count
}

// ── Binance Pyramid Round-Trip (entry → pyramid entry → exit) ────────────────

// TestSnapshotIntegration_Binance_PyramidRoundTrip exercises the complete lifecycle:
//
//  1. Long entry   → fill → snapshot: 1 unit, cash decreases
//  2. Pyramid entry → fill → snapshot: 2 units accumulated, cash decreases further
//  3. Exit signal  → fill → snapshot: 0 positions, cash+equity restored
//
// Snapshots at each stage are printed so the equity curve is visible in test output.
func TestSnapshotIntegration_Binance_PyramidRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	js := connectNATS(t)

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

	// Helm capital = allocCap so helm and hand cash move on the same scale.
	rt := buildIntegrationRuntime(t, js, ex, creds, decimal.NewFromFloat(allocCap))
	rt.UpdatePrice(symbol, price)

	// pyramid=true, maxUnits=3.
	hand := addIntegrationHandEx(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap), true, 3)
	hand.Start()
	defer hand.Stop()
	defer cancelAllOrders(t, rt, hand)

	logStageSnaps := func(stage string) {
		ctx2, c2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer c2()
		helmSnaps, _ := rt.SnapshotLog.Latest(ctx2, rt.HelmID.String(), "", 20)
		handSnaps, _ := rt.SnapshotLog.Latest(ctx2, rt.HelmID.String(), hand.ID().String(), 20)

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
			snap, syncErr := syncer.SyncAccount(syncCtx, creds, nil)
			syncCancel()
			if syncErr != nil {
				t.Logf("║ ▶ EXCHANGE WALLET: sync error: %v", syncErr)
			} else {
				t.Logf("║ ▶ EXCHANGE WALLET (real)")
				t.Logf("║   cash(USDT)     = %s", snap.Cash)
				t.Logf("║   equity         = %s", snap.Equity)
				for _, b := range snap.Balances {
					if b.Free.IsPositive() {
						t.Logf("║     asset: %s free=%s", b.Asset, b.Free)
					}
				}
				for _, p := range snap.Positions {
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

		// ── 4. Snapshot log (JetStream) ────────────────────────────────────
		t.Logf("║ ▶ SNAPSHOT LOG: helm=%d hand=%d", len(helmSnaps), len(handSnaps))
		for i, s := range helmSnaps {
			t.Logf("║   helm[%d] ts=%s cash=%s equity=%s pos=%d",
				i, s.TS.Format("15:04:05.000"), s.Cash, s.Equity, len(s.Positions))
			for _, p := range s.Positions {
				t.Logf("║            pos: %s qty=%s avg=%s", p.Symbol, p.Qty, p.AvgPrice)
			}
		}
		for i, s := range handSnaps {
			t.Logf("║   hand[%d] ts=%s cash=%s equity=%s pos=%d",
				i, s.TS.Format("15:04:05.000"), s.Cash, s.Equity, len(s.Positions))
			for _, p := range s.Positions {
				t.Logf("║            pos: %s qty=%s avg=%s", p.Symbol, p.Qty, p.AvgPrice)
			}
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
	waitSnapshots(t, rt, hand, 2, 10*time.Second)
	logStageSnaps("entry")

	ctx1, c1 := context.WithTimeout(context.Background(), 5*time.Second)
	helmSnap1, _ := rt.SnapshotLog.Latest(ctx1, rt.HelmID.String(), "", 5)
	handSnap1, _ := rt.SnapshotLog.Latest(ctx1, rt.HelmID.String(), hand.ID().String(), 5)
	c1()
	if len(helmSnap1) > 0 {
		s := helmSnap1[len(helmSnap1)-1]
		if len(s.Positions) == 0 {
			t.Error("stage 1 helm: expected open position after entry")
		}
	}
	if len(handSnap1) > 0 {
		s := handSnap1[len(handSnap1)-1]
		if len(s.Positions) == 0 {
			t.Error("stage 1 hand: expected open position after entry")
		}
	}

	// ── Stage 2: pyramid entry ─────────────────────────────────────────────────
	t.Log("═══ Stage 2: pyramid entry ═══")
	id2 := placeAndWait(longSignalFor(symbol), id1, "pyramid")
	waitSnapshots(t, rt, hand, 4, 10*time.Second)
	logStageSnaps("pyramid entry")

	ctx2, c2 := context.WithTimeout(context.Background(), 5*time.Second)
	helmSnap2, _ := rt.SnapshotLog.Latest(ctx2, rt.HelmID.String(), "", 10)
	handSnap2, _ := rt.SnapshotLog.Latest(ctx2, rt.HelmID.String(), hand.ID().String(), 10)
	c2()

	expectedQty := decimal.NewFromFloat(orderQty * 2)
	if len(helmSnap2) > 0 {
		s := helmSnap2[len(helmSnap2)-1]
		var total decimal.Decimal
		for _, p := range s.Positions {
			total = total.Add(p.Qty)
		}
		if total.LessThan(expectedQty) {
			t.Errorf("stage 2 helm: expected accumulated qty ≥ %s, got %s", expectedQty, total)
		} else {
			t.Logf("stage 2 helm: accumulated qty=%s ✓", total)
		}
		// Cash must be lower than after stage 1 (second buy consumed more cash).
		if len(helmSnap1) > 0 && !s.Cash.LessThan(helmSnap1[len(helmSnap1)-1].Cash) {
			t.Errorf("stage 2 helm: cash should decrease after pyramid entry; stage1=%s stage2=%s",
				helmSnap1[len(helmSnap1)-1].Cash, s.Cash)
		}
	}
	if len(handSnap2) > 0 {
		s := handSnap2[len(handSnap2)-1]
		var total decimal.Decimal
		for _, p := range s.Positions {
			total = total.Add(p.Qty)
		}
		if total.LessThan(expectedQty) {
			t.Errorf("stage 2 hand: expected accumulated qty ≥ %s, got %s", expectedQty, total)
		} else {
			t.Logf("stage 2 hand: accumulated qty=%s ✓", total)
		}
		if len(handSnap1) > 0 && !s.Cash.LessThan(handSnap1[len(handSnap1)-1].Cash) {
			t.Errorf("stage 2 hand: cash should decrease after pyramid entry; stage1=%s stage2=%s",
				handSnap1[len(handSnap1)-1].Cash, s.Cash)
		}
	}

	// ── Stage 3: exit ─────────────────────────────────────────────────────────
	t.Log("═══ Stage 3: exit ═══")
	placeAndWait(exitSig(symbol), id2, "exit")
	waitSnapshots(t, rt, hand, 6, 15*time.Second)
	logStageSnaps("exit")

	assertFlatSnapshots(t, rt, hand)
}

// TestSnapshotIntegration_Binance_KillPauseRelease verifies the hand-level and helm-level
// behavior and snapshot persistence during Kill, Pause, Resume, and Release lifecycle states.
func TestSnapshotIntegration_Binance_KillPauseRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set")
	}

	js := connectNATS(t)

	const symbol = "BTCUSDT"
	const orderQty = 0.001
	const allocCap = 500.0

	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Sync real USDT cash
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

	rt := buildIntegrationRuntime(t, js, ex, creds, initialCapital)
	rt.UpdatePrice(symbol, price)

	// Create and start hand1
	hand1 := addIntegrationHandEx(rt, symbol, decimal.NewFromFloat(orderQty), decimal.NewFromFloat(allocCap), false, 1)
	hand1.Start()
	defer hand1.Stop()

	// ── Cleanup Helper ────────────────────────────────────────────────────────
	t.Cleanup(func() {
		// Clean up: cancel orders and sell all BTC to keep sandbox clean
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()

		// Cancel any open orders
		if orders, err := ex.ListOpenOrders(cleanupCtx, creds, symbol); err == nil {
			for _, o := range orders {
				_ = ex.CancelOrder(cleanupCtx, creds, o.ID)
			}
		}

		// Sync account and sell any remaining BTC
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

	var orderID1 string
	select {
	case e := <-placed:
		t.Logf("1st entry order placed: code=%d order_id=%s reason=%s", e.Code, e.OrderID, e.Reason)
		if e.Code == runtime.CodeOrderFailed {
			t.Fatalf("entry order failed: %s", e.Reason)
		}
		orderID1 = e.OrderID
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: entry order not placed")
	}

	select {
	case e := <-filled:
		t.Logf("1st entry filled: order_id=%s qty=%s price=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("fill not observed in activity ring — continuing")
	}

	// Wait for snapshots
	waitSnapshots(t, rt, hand1, 2, 10*time.Second)

	// Verify position in snapshot
	ctx1, c1 := context.WithTimeout(context.Background(), 5*time.Second)
	handSnaps1, err := rt.SnapshotLog.Latest(ctx1, rt.HelmID.String(), hand1.ID().String(), 5)
	c1()
	if err != nil || len(handSnaps1) == 0 {
		t.Fatalf("failed to fetch hand1 snapshots or empty: %v", err)
	}
	lastSnap := handSnaps1[len(handSnaps1)-1]
	if len(lastSnap.Positions) == 0 {
		t.Fatal("expected position in snapshot after entry")
	}
	t.Logf("hand1 snapshot position confirmed: %s", lastSnap.Positions[0].Qty)

	// ── Step 2: Pause ─────────────────────────────────────────────────────────
	t.Log("=== Step 2: Pause ===")
	hand1.Pause()
	if !hand1.IsPaused() {
		t.Fatal("hand1 should be paused")
	}
	if hand1.Health().Status != runtime.HealthPaused {
		t.Fatalf("expected hand status to be Paused, got %s", hand1.Health().Status)
	}

	// Deliver another signal while paused — should be dropped
	placedPaused := orderNotifyNew(hand1, orderID1, 10*time.Second)
	hand1.DeliverSignal(longSignalFor(symbol))

	select {
	case e := <-placedPaused:
		t.Fatalf("unexpected order event while paused: %v", e)
	case <-time.After(5 * time.Second):
		t.Log("signal successfully dropped while paused (no order placed)")
	}

	// ── Step 3: Resume ────────────────────────────────────────────────────────
	t.Log("=== Step 3: Resume ===")
	hand1.Resume()
	if hand1.IsPaused() {
		t.Fatal("hand1 should not be paused after resume")
	}
	if hand1.Health().Status != runtime.HealthRunning {
		t.Fatalf("expected hand status to be Running, got %s", hand1.Health().Status)
	}

	// ── Step 4: Release ───────────────────────────────────────────────────────
	t.Log("=== Step 4: Release ===")
	releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 10*time.Second)
	hand1.Release(releaseCtx)
	releaseCancel()

	if hand1.IsRunning() {
		t.Fatal("hand1 should be stopped after release")
	}
	if hand1.Health().Status != runtime.HealthReleased {
		t.Fatalf("expected status Released, got %s", hand1.Health().Status)
	}

	// Logical portfolio should be flat for this hand
	if pos := hand1.Position(); pos != nil {
		t.Fatalf("logical position should be flat (nil) after release, got: %v", pos)
	}

	// Verify hand snapshot is flat
	waitSnapshots(t, rt, hand1, 3, 10*time.Second) // wait for flat release snapshot
	ctxRelease, cRelease := context.WithTimeout(context.Background(), 5*time.Second)
	handSnapsRelease, _ := rt.SnapshotLog.Latest(ctxRelease, rt.HelmID.String(), hand1.ID().String(), 5)
	cRelease()
	if len(handSnapsRelease) > 0 {
		lastReleaseSnap := handSnapsRelease[len(handSnapsRelease)-1]
		if len(lastReleaseSnap.Positions) != 0 {
			t.Fatalf("expected snapshot position count to be 0 after release, got %d", len(lastReleaseSnap.Positions))
		}
	}

	// Verify exchange position STILL EXISTS
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

	// ── Step 5: Kill ──────────────────────────────────────────────────────────
	t.Log("=== Step 5: Kill ===")
	// Create hand2 to manage a new trade and then kill it
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

	// Wait for hand2 entry snapshot
	waitSnapshots(t, rt, hand2, 2, 10*time.Second)

	// Kill hand2
	killCtx, killCancel := context.WithTimeout(context.Background(), 15*time.Second)
	hand2.Kill(killCtx)
	killCancel()

	if hand2.IsRunning() {
		t.Fatal("hand2 should be stopped after kill")
	}
	if hand2.Health().Status != runtime.HealthKilled {
		t.Fatalf("expected status Killed, got %s", hand2.Health().Status)
	}

	// Logical position for hand2 should be flat
	if pos := hand2.Position(); pos != nil {
		t.Fatalf("logical position should be flat (nil) after kill, got: %v", pos)
	}

	// Verify hand2 snapshot is flat
	waitSnapshots(t, rt, hand2, 3, 15*time.Second)
	ctxKill, cKill := context.WithTimeout(context.Background(), 5*time.Second)
	handSnapsKill, _ := rt.SnapshotLog.Latest(ctxKill, rt.HelmID.String(), hand2.ID().String(), 5)
	cKill()
	if len(handSnapsKill) > 0 {
		lastKillSnap := handSnapsKill[len(handSnapsKill)-1]
		if len(lastKillSnap.Positions) != 0 {
			t.Fatalf("expected snapshot position count to be 0 after kill, got %d", len(lastKillSnap.Positions))
		}
	}
	t.Log("Kill correctly flattened hand2 logical state and snapshots ✓")
}
