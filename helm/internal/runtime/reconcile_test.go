package runtime_test

// Reconciler crash-scenario tests.
//
// Scenarios covered:
//   1. Flat hand (idle poslog) → ReconcileSkipped
//   2. Pending entry, order still open at exchange → ReconcileRestored
//   3. Crash between order_placed and order_filled; exchange reports "filled" → ReconcileFillApplied
//   4. Crash between order_placed and order_filled; exchange reports "cancelled" → ReconcileCancelled
//   5. Open position confirmed at exchange → ReconcileRestored
//   6. Open position gone from exchange (external close) → ReconcileExternalClose, deterministic Event.ID
//   7. External-close reconcile run twice → same Event.ID both times (idempotent by design)
//   8. Exiting phase; close order still pending → ReconcileRestored
//   9. Exiting phase; close order filled while down → ReconcileFillApplied
//
// Dependencies are all in-process fakes — no NATS, no real exchange.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// ── fakes ─────────────────────────────────────────────────────────────────────

// fakePosLog implements poslog.Log in-memory.
type fakePosLog struct {
	mu        sync.Mutex
	events    []poslog.Event // events returned by ReplayHand
	published []poslog.Event // events captured by Publish
}

func (f *fakePosLog) Publish(_ context.Context, e poslog.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, e)
	return nil
}

func (f *fakePosLog) ReplayHand(_ context.Context, _, _ string) ([]poslog.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]poslog.Event, len(f.events))
	copy(cp, f.events)
	return cp, nil
}

func (f *fakePosLog) ReplayLeg(_ context.Context, _, _, _ string) ([]poslog.Event, error) {
	return nil, nil
}

func (f *fakePosLog) publishedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.published))
	for _, e := range f.published {
		ids = append(ids, e.ID)
	}
	return ids
}

// fakeExchange implements exchange.Exchange in-memory.
type fakeExchange struct {
	openOrders map[string]exchange.OrderResult // orderID → result (in "open" state)
	positions  map[string]exchange.PositionResult
	orderByID  map[string]exchange.OrderResult // all terminal orders (filled/cancelled)
}

func (f *fakeExchange) Name() string { return "fake" }

func (f *fakeExchange) PlaceOrder(_ context.Context, _ exchange.Credentials, _ exchange.OrderRequest) (*exchange.OrderResult, error) {
	panic("fakeExchange.PlaceOrder not expected in reconciler tests")
}

func (f *fakeExchange) GetOrder(_ context.Context, _ exchange.Credentials, orderID string) (*exchange.OrderResult, error) {
	if r, ok := f.orderByID[orderID]; ok {
		return &r, nil
	}
	return &exchange.OrderResult{ID: orderID, Status: "cancelled"}, nil
}

func (f *fakeExchange) CancelOrder(_ context.Context, _ exchange.Credentials, _ string) error {
	panic("fakeExchange.CancelOrder not expected in reconciler tests")
}

func (f *fakeExchange) ListOpenOrders(_ context.Context, _ exchange.Credentials, _ string) ([]exchange.OrderResult, error) {
	out := make([]exchange.OrderResult, 0, len(f.openOrders))
	for _, r := range f.openOrders {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeExchange) ListPositions(_ context.Context, _ exchange.Credentials) ([]exchange.PositionResult, error) {
	out := make([]exchange.PositionResult, 0, len(f.positions))
	for _, r := range f.positions {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeExchange) SubscribeFills(_ context.Context, _ exchange.Credentials) (<-chan exchange.FillEvent, error) {
	ch := make(chan exchange.FillEvent)
	close(ch)
	return ch, nil
}

// ── builder helpers ────────────────────────────────────────────────────────────

func buildRuntime(ex exchange.Exchange, log poslog.Log) *runtime.HelmRuntime {
	helmID := uuid.New()
	accountID := uuid.New()
	userID := uuid.New()
	pf := portfolio.New(decimal.Zero)
	rm := risk.New(risk.DefaultConfig(), pf)
	ob := orderbook.NewOrderBook("fake")
	rt := runtime.NewHelmRuntime(helmID, accountID, userID, "fake", pf, rm, ob, ex, exchange.Credentials{}, nil)
	rt.PosLog = log
	return rt
}

func addHand(rt *runtime.HelmRuntime, pyramid bool, maxUnits int) *runtime.Hand {
	handID := uuid.New()
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.DefaultSizingConfig())
	h := runtime.NewHand(handID, rt.HelmID, rt, strat, tact, pyramid, maxUnits, 10*time.Second, nil)
	rt.AddHand(h)
	return h
}

// poslog event helpers (mirror state_test.go helpers, duplicated here to keep tests self-contained)

func recPlaced(orderID, positionID, symbol, side string, isClose bool) poslog.Event {
	p := poslog.OrderPlacedPayload{
		OrderID: orderID, Symbol: symbol, Side: side, IsClose: isClose,
		Qty: "0.1", StopLoss: "29000",
	}
	b, _ := json.Marshal(p)
	return poslog.Event{ID: orderID, PositionID: positionID, Kind: poslog.KindOrderPlaced, Payload: b, At: time.Now()}
}

func recFilled(orderID, positionID string, price string) poslog.Event {
	p := poslog.OrderFilledPayload{OrderID: orderID, FillPrice: price, FillQty: "0.1", Source: "ws"}
	b, _ := json.Marshal(p)
	return poslog.Event{ID: orderID + "_filled", PositionID: positionID, Kind: poslog.KindOrderFilled, Payload: b, At: time.Now()}
}

// ── tests ──────────────────────────────────────────────────────────────────────

// Scenario 1: hand's poslog is empty → nothing to reconcile.
func TestReconcile_FlatHand(t *testing.T) {
	log := &fakePosLog{}
	ex := &fakeExchange{openOrders: map[string]exchange.OrderResult{}, positions: map[string]exchange.PositionResult{}}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := runtime.NewReconciler(log).Reconcile(context.Background(), rt)

	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Action != runtime.ReconcileSkipped {
		t.Fatalf("want ReconcileSkipped, got %s", results[0].Action)
	}
	if len(log.publishedIDs()) != 0 {
		t.Fatal("want no published events for flat hand")
	}
}

// Scenario 2: entry order placed, still open at exchange → nothing to do.
func TestReconcile_PendingOrderStillOpen(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{recPlaced("ord1", "ord1", "BTCUSDT", "buy", false)},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{
			"ord1": {ID: "ord1", Status: "new", Symbol: "BTCUSDT"},
		},
		positions: map[string]exchange.PositionResult{},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := runtime.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != runtime.ReconcileRestored {
		t.Fatalf("want ReconcileRestored, got %s", results[0].Action)
	}
	if len(log.publishedIDs()) != 0 {
		t.Fatal("want no events published — order still pending")
	}
}

// Scenario 3: crash between order_placed and order_filled.
// Exchange confirms the order was filled while we were down → emit order_filled event.
func TestReconcile_CrashBetweenPlacedAndFilled(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{recPlaced("ord1", "ord1", "BTCUSDT", "buy", false)},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{}, // order no longer open
		positions:  map[string]exchange.PositionResult{},
		orderByID: map[string]exchange.OrderResult{
			"ord1": {ID: "ord1", Status: "filled", Symbol: "BTCUSDT", FilledAvg: decimal.NewFromInt(30000), FilledQty: decimal.NewFromFloat(0.1)},
		},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := runtime.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != runtime.ReconcileFillApplied {
		t.Fatalf("want ReconcileFillApplied, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	if len(ids) != 1 {
		t.Fatalf("want 1 published event, got %d", len(ids))
	}
	if ids[0] != "ord1_filled" {
		t.Fatalf("published event ID: want ord1_filled, got %s", ids[0])
	}
}

// Scenario 4: entry order placed but cancelled at exchange while down → emit order_cancelled.
func TestReconcile_CrashWithCancelledOrder(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{recPlaced("ord1", "ord1", "BTCUSDT", "buy", false)},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{},
		positions:  map[string]exchange.PositionResult{},
		orderByID: map[string]exchange.OrderResult{
			"ord1": {ID: "ord1", Status: "cancelled"},
		},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := runtime.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != runtime.ReconcileCancelled {
		t.Fatalf("want ReconcileCancelled, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	if len(ids) != 1 || ids[0] != "ord1_cancelled" {
		t.Fatalf("published event: want [ord1_cancelled], got %v", ids)
	}
}

// Scenario 5: hand has an open position; exchange confirms it still exists → no action.
func TestReconcile_OpenPositionConfirmed(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlaced("ord1", "ord1", "BTCUSDT", "buy", false),
			recFilled("ord1", "ord1", "30000"),
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{},
		positions: map[string]exchange.PositionResult{
			"BTCUSDT": {Symbol: "BTCUSDT", Qty: decimal.NewFromFloat(0.1), AvgPrice: decimal.NewFromInt(30000)},
		},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := runtime.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != runtime.ReconcileRestored {
		t.Fatalf("want ReconcileRestored, got %s", results[0].Action)
	}
	if len(log.publishedIDs()) != 0 {
		t.Fatal("want no events published — position confirmed at exchange")
	}
}

// Scenario 6: position gone from exchange (external close / liquidation) → emit position_closed.
func TestReconcile_ExternalClose(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlaced("ord1", "ord1", "BTCUSDT", "buy", false),
			recFilled("ord1", "ord1", "30000"),
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{},
		positions:  map[string]exchange.PositionResult{}, // position gone
	}
	rt := buildRuntime(ex, log)
	h := addHand(rt, false, 1)

	results := runtime.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != runtime.ReconcileExternalClose {
		t.Fatalf("want ReconcileExternalClose, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	if len(ids) != 1 {
		t.Fatalf("want 1 published event, got %d: %v", len(ids), ids)
	}
	// Event ID must be deterministic (no timestamp component).
	wantID := h.ID().String() + "_ext_" + "ord1"
	if ids[0] != wantID {
		t.Fatalf("Event.ID: want %q (deterministic), got %q", wantID, ids[0])
	}
}

// Scenario 7: reconciler runs twice for the same external-close scenario.
// Both runs must publish the same Event.ID — JetStream dedup handles the rest.
func TestReconcile_ExternalClose_Idempotent(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlaced("ord1", "ord1", "BTCUSDT", "buy", false),
			recFilled("ord1", "ord1", "30000"),
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{},
		positions:  map[string]exchange.PositionResult{},
	}
	rt := buildRuntime(ex, log)
	h := addHand(rt, false, 1)

	rec := runtime.NewReconciler(log)
	rec.Reconcile(context.Background(), rt)
	// Second run: poslog still returns same events (dedup happens inside JetStream, not here).
	rec.Reconcile(context.Background(), rt)

	ids := log.publishedIDs()
	if len(ids) != 2 {
		t.Fatalf("want 2 publish calls (one per run), got %d", len(ids))
	}
	wantID := h.ID().String() + "_ext_" + "ord1"
	if ids[0] != wantID || ids[1] != wantID {
		t.Fatalf("both publish calls must use same deterministic ID %q, got %v", wantID, ids)
	}
}

// Scenario 8: close order was placed but not yet filled; order still pending at exchange.
func TestReconcile_ExitingPhase_OrderStillOpen(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlaced("ord1", "ord1", "BTCUSDT", "buy", false),
			recFilled("ord1", "ord1", "30000"),
			recPlaced("ord2", "ord1", "", "", true), // close order
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{
			"ord2": {ID: "ord2", Status: "new"},
		},
		positions: map[string]exchange.PositionResult{
			"BTCUSDT": {Symbol: "BTCUSDT", Qty: decimal.NewFromFloat(0.1)},
		},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := runtime.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != runtime.ReconcileRestored {
		t.Fatalf("want ReconcileRestored, got %s", results[0].Action)
	}
}

// Scenario 9: close order was filled while app was down → emit order_filled.
func TestReconcile_ExitingPhase_CloseFilled(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlaced("ord1", "ord1", "BTCUSDT", "buy", false),
			recFilled("ord1", "ord1", "30000"),
			recPlaced("ord2", "ord1", "", "", true), // close order placed
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{}, // close order gone
		positions:  map[string]exchange.PositionResult{},
		orderByID: map[string]exchange.OrderResult{
			"ord2": {ID: "ord2", Status: "filled", FilledAvg: decimal.NewFromInt(31000), FilledQty: decimal.NewFromFloat(0.1)},
		},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := runtime.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != runtime.ReconcileFillApplied {
		t.Fatalf("want ReconcileFillApplied, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	if len(ids) != 1 || ids[0] != "ord2_filled" {
		t.Fatalf("published: want [ord2_filled], got %v", ids)
	}
}
