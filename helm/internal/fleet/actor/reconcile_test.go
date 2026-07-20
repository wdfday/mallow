package actor_test

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

	"mallow/helm/internal/module/hand/domain"

	"mallow/helm/internal/fleet/actor"
	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/risk"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/core/tactics"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
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

func (f *fakePosLog) TradesPaged(_ context.Context, _, _ string, _ uint64, _ int) (poslog.TradesPage, error) {
	return poslog.TradesPage{}, nil
}

func (f *fakePosLog) PurgeHand(_ context.Context, _, _ string) error {
	return nil
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
	openOrders           map[string]exchange.OrderResult // orderID → result (in "open" state)
	positions            map[string]exchange.PositionResult
	orderByID            map[string]exchange.OrderResult // all terminal orders (filled/cancelled)
	orderByClientOrderID map[string]exchange.OrderResult
	// exitOrdersByClientOrderID, keyed by clid, backs GetExitOrderByClientOrderID
	// (exchange.ExitOrderQuerier) — used by the ambiguous-bracket reconcile tests.
	exitOrdersByClientOrderID map[string][]exchange.OrderResult
}

// GetExitOrderByClientOrderID implements exchange.ExitOrderQuerier.
func (f *fakeExchange) GetExitOrderByClientOrderID(_ context.Context, _ exchange.Credentials, _ string, _ exchange.MarketKind, clientOrderID string) ([]exchange.OrderResult, error) {
	return f.exitOrdersByClientOrderID[clientOrderID], nil
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

func (f *fakeExchange) GetOrderByClientOrderID(_ context.Context, _ exchange.Credentials, _ string, _ exchange.MarketKind, clientOrderID string) (*exchange.OrderResult, error) {
	if r, ok := f.orderByClientOrderID[clientOrderID]; ok {
		return &r, nil
	}
	return nil, nil
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

// ── builder helpers ────────────────────────────────────────────────────────────

func buildRuntime(ex exchange.Exchange, log poslog.Log) *actor.HelmRuntime {
	helmID := uuid.New()
	accountID := uuid.New()
	userID := uuid.New()
	pf := portfolio.New(decimal.Zero)
	rm := risk.New(risk.DefaultConfig(), pf)
	rt := actor.NewHelmRuntime(helmID, accountID, userID, "fake", pf, rm, ex, exchange.Credentials{}, nil, time.Now())
	rt.PosLog = log
	return rt
}

func addHand(rt *actor.HelmRuntime, pyramid bool, maxUnits int) *actor.Hand {
	handID := uuid.New()
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.DefaultSizingConfig())
	h := actor.NewHand(handID, rt.HelmID, rt, strat, tact, pyramid, maxUnits, 10*time.Second, nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	rt.AddHand(h, &domain.Hand{ID: h.ID(), HelmID: rt.HelmID, Symbols: domain.StringSlice{h.Symbol}})
	return h
}

// poslog event helpers (mirror state_test.go helpers, duplicated here to keep tests self-contained)

func recPlace(clientOrderID, tradeID, symbol, side string, isClose bool) poslog.Event {
	p := poslog.OrderPlacePayload{
		ClientOrderID: clientOrderID, Symbol: symbol, Side: side, IsClose: isClose,
		Qty: "0.1", StopLoss: "29000", Price: "30000", OrderType: "limit",
	}
	b, _ := json.Marshal(p)
	return poslog.Event{ID: clientOrderID, TradeID: tradeID, Kind: poslog.KindOrderPlace, Payload: b, At: time.Now()}
}

func recPlaced(orderID, tradeID, symbol, side string, isClose bool) poslog.Event {
	p := poslog.OrderPlacedPayload{
		OrderID: orderID, Symbol: symbol, Side: side, IsClose: isClose,
		Qty: "0.1", StopLoss: "29000", Price: "30000", OrderType: "limit",
	}
	b, _ := json.Marshal(p)
	return poslog.Event{ID: orderID, TradeID: tradeID, Kind: poslog.KindOrderPlaced, Payload: b, At: time.Now()}
}

func recFilled(orderID, tradeID string, price string) poslog.Event {
	p := poslog.OrderFilledPayload{OrderID: orderID, FillPrice: price, FillQty: "0.1", Source: "ws"}
	b, _ := json.Marshal(p)
	return poslog.Event{ID: orderID + "_filled", TradeID: tradeID, Kind: poslog.KindOrderFilled, Payload: b, At: time.Now()}
}

func recBracketPlace(clientOrderID, tradeID, symbol string) poslog.Event {
	p := poslog.BracketPlacePayload{Symbol: symbol, ClientOrderID: clientOrderID, StopLoss: "29000", Qty: "0.1"}
	b, _ := json.Marshal(p)
	return poslog.Event{ID: tradeID + "_bracket_place_" + clientOrderID, TradeID: tradeID, Kind: poslog.KindBracketPlace, Payload: b, At: time.Now()}
}

// ── tests ──────────────────────────────────────────────────────────────────────

// Scenario 1: hand's poslog is empty → nothing to reconcile.
func TestReconcile_FlatHand(t *testing.T) {
	log := &fakePosLog{}
	ex := &fakeExchange{openOrders: map[string]exchange.OrderResult{}, positions: map[string]exchange.PositionResult{}}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Action != actor.ReconcileSkipped {
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

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileRestored {
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

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileFillApplied {
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

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileCancelled {
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

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileRestored {
		t.Fatalf("want ReconcileRestored, got %s", results[0].Action)
	}
	if len(log.publishedIDs()) != 0 {
		t.Fatal("want no events published — position confirmed at exchange")
	}
}

// TestReconcile_AmbiguousBracket_FoundAtExchange: crash left a KindBracketPlace
// with no matching KindBracketPlaced. The exchange confirms the bracket landed
// (GetExitOrderByClientOrderID finds it) — reconcile must emit KindBracketPlaced
// to restore ExchangeOrderIDs, same as a normal successful placement would.
func TestReconcile_AmbiguousBracket_FoundAtExchange(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlaced("ord1", "ord1", "BTCUSDT", "buy", false),
			recFilled("ord1", "ord1", "30000"),
			recBracketPlace("mlwabc123", "ord1", "BTCUSDT"),
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{},
		positions: map[string]exchange.PositionResult{
			"BTCUSDT": {Symbol: "BTCUSDT", Qty: decimal.NewFromFloat(0.1), AvgPrice: decimal.NewFromInt(30000)},
		},
		exitOrdersByClientOrderID: map[string][]exchange.OrderResult{
			"mlwabc123": {{ID: "BTCUSDT:99", Symbol: "BTCUSDT", Status: "new"}},
		},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileRestored {
		t.Fatalf("want ReconcileRestored, got %s", results[0].Action)
	}
	var sawBracketPlaced bool
	for _, e := range log.published {
		if e.Kind == poslog.KindBracketPlaced {
			sawBracketPlaced = true
			var p poslog.BracketPlacedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				t.Fatalf("unmarshal BracketPlacedPayload: %v", err)
			}
			if len(p.OrderIDs) != 1 || p.OrderIDs[0] != "BTCUSDT:99" {
				t.Errorf("want OrderIDs=[BTCUSDT:99], got %v", p.OrderIDs)
			}
			if p.ClientOrderID != "mlwabc123" {
				t.Errorf("want ClientOrderID=mlwabc123, got %s", p.ClientOrderID)
			}
		}
	}
	if !sawBracketPlaced {
		t.Fatal("want KindBracketPlaced emitted — exchange confirmed the bracket landed")
	}
}

// TestReconcile_AmbiguousBracket_NotFound: crash left a KindBracketPlace with no
// matching KindBracketPlaced, and the exchange has no record of it either
// (never actually reached the exchange, or GetExitOrderByClientOrderID is
// unconfigured/empty). Reconcile must NOT emit KindBracketPlaced and must still
// restore the position (unprotected — the local exit monitor becomes the only net).
func TestReconcile_AmbiguousBracket_NotFound(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlaced("ord1", "ord1", "BTCUSDT", "buy", false),
			recFilled("ord1", "ord1", "30000"),
			recBracketPlace("mlwabc123", "ord1", "BTCUSDT"),
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{},
		positions: map[string]exchange.PositionResult{
			"BTCUSDT": {Symbol: "BTCUSDT", Qty: decimal.NewFromFloat(0.1), AvgPrice: decimal.NewFromInt(30000)},
		},
		exitOrdersByClientOrderID: map[string][]exchange.OrderResult{}, // nothing found
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileRestored {
		t.Fatalf("want ReconcileRestored, got %s", results[0].Action)
	}
	for _, e := range log.published {
		if e.Kind == poslog.KindBracketPlaced {
			t.Fatal("want no KindBracketPlaced — exchange has no record of this bracket")
		}
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

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileExternalClose {
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

	rec := actor.NewReconciler(log)
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

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileRestored {
		t.Fatalf("want ReconcileRestored, got %s", results[0].Action)
	}
}

// Scenario 10: entry order partially_filled (not in open-orders batch but GetOrder returns
// "partially_filled") → ReconcileRestored; order restored into hand.orders for pollOrders.
func TestReconcile_PartiallyFilledOrder_Restored(t *testing.T) {
	const orderID = "ord1"
	log := &fakePosLog{
		events: []poslog.Event{recPlaced(orderID, orderID, "BTCUSDT", "buy", false)},
	}
	// Not in openOrders (missed by batch fetch), but GetOrder says partially_filled.
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{}, // empty batch fetch
		positions:  map[string]exchange.PositionResult{},
		orderByID: map[string]exchange.OrderResult{
			orderID: {
				ID:        orderID,
				Status:    "partially_filled",
				Symbol:    "BTCUSDT",
				FilledQty: decimal.NewFromFloat(0.05),
				FilledAvg: decimal.NewFromFloat(30100),
			},
		},
	}
	rt := buildRuntime(ex, log)
	hand := addHand(rt, false, 1)

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].Action != actor.ReconcileRestored {
		t.Fatalf("want ReconcileRestored, got %s", results[0].Action)
	}
	// No poslog events should be emitted for a still-open order.
	if ids := log.publishedIDs(); len(ids) != 0 {
		t.Fatalf("want no published events, got %v", ids)
	}
	// Order should be tracked in the helm orderHandMap for future WS routing.
	if !rt.HasOrderTracking(orderID) {
		t.Error("expected order to be tracked in orderHandMap after partial-fill restore")
	}
	// Order should be in hand.orders so pollOrders can keep watching it.
	orders := hand.Orders()
	found := false
	for _, o := range orders {
		if o.ID == orderID {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected order to be in hand.orders after partial-fill restore")
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

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileFillApplied {
		t.Fatalf("want ReconcileFillApplied, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	if len(ids) != 1 || ids[0] != "ord2_filled" {
		t.Fatalf("published: want [ord2_filled], got %v", ids)
	}
}

// Scenario 10: Crash after KindOrderPlace, order is still open at exchange.
// Reconciler queries clid, finds it open, and emits KindOrderPlaced to bind it.
func TestReconcile_WAL_PreFlight_Open(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlace("mlwclid1", "mlwclid1", "BTCUSDT", "buy", false),
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{
			"ord1": {ID: "ord1", ClientOrderID: "mlwclid1", Status: "new", Symbol: "BTCUSDT", Qty: decimal.NewFromFloat(0.1)},
		},
		positions: map[string]exchange.PositionResult{},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileRestored {
		t.Fatalf("want ReconcileRestored, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	// Should have published KindOrderPlaced to bind client_order_id to exchange order_id
	if len(ids) != 1 || ids[0] != "ord1" {
		t.Fatalf("published: want [ord1], got %v", ids)
	}
}

// Scenario 11: Crash after KindOrderPlace, order was filled while app was down.
// Reconciler queries clid, finds it filled, emits both KindOrderPlaced and KindOrderFilled.
func TestReconcile_WAL_PreFlight_Filled(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlace("mlwclid1", "mlwclid1", "BTCUSDT", "buy", false),
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{}, // terminal state: not in openOrders
		positions:  map[string]exchange.PositionResult{},
		orderByClientOrderID: map[string]exchange.OrderResult{
			"mlwclid1": {ID: "ord1", ClientOrderID: "mlwclid1", Status: "filled", Symbol: "BTCUSDT", Qty: decimal.NewFromFloat(0.1), FilledQty: decimal.NewFromFloat(0.1), FilledAvg: decimal.NewFromInt(30000)},
		},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileFillApplied {
		t.Fatalf("want ReconcileFillApplied, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	// Should have published both KindOrderPlaced and KindOrderFilled (ord1_filled)
	if len(ids) != 2 || ids[0] != "ord1" || ids[1] != "ord1_filled" {
		t.Fatalf("published: want [ord1, ord1_filled], got %v", ids)
	}
}

// Scenario 12: Crash after KindOrderPlace, order never reached the exchange.
// Reconciler queries clid, finds nothing, and emits KindOrderCancelled to revert to Idle.
func TestReconcile_WAL_PreFlight_NotFound(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlace("mlwclid1", "mlwclid1", "BTCUSDT", "buy", false),
		},
	}
	ex := &fakeExchange{
		openOrders:           map[string]exchange.OrderResult{},
		positions:            map[string]exchange.PositionResult{},
		orderByClientOrderID: map[string]exchange.OrderResult{}, // empty (not found)
	}
	rt := buildRuntime(ex, log)
	addHand(rt, false, 1)

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileCancelled {
		t.Fatalf("want ReconcileCancelled, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	// Should have published KindOrderCancelled (mlwclid1_cancelled)
	if len(ids) != 1 || ids[0] != "mlwclid1_cancelled" {
		t.Fatalf("published: want [mlwclid1_cancelled], got %v", ids)
	}
}

func recPlaceAdd(clientOrderID, tradeID, symbol, side string) poslog.Event {
	p := poslog.OrderPlacePayload{
		ClientOrderID: clientOrderID, Symbol: symbol, Side: side,
		Qty: "0.1", StopLoss: "29000", Price: "30000", OrderType: "limit",
		IsPyramidAdd: true,
	}
	b, _ := json.Marshal(p)
	return poslog.Event{ID: clientOrderID, TradeID: tradeID, Kind: poslog.KindOrderPlace, Payload: b, At: time.Now()}
}

// Scenario 13: Crash after KindOrderPlace of a pyramid add. Order still open at exchange.
func TestReconcile_WAL_PreFlight_PyramidAdd_Open(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlaced("ord1", "ord1", "BTCUSDT", "buy", false),
			recFilled("ord1", "ord1", "30000"),
			recPlaceAdd("mlwclid2", "ord1", "BTCUSDT", "buy"),
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{
			"ord2": {ID: "ord2", ClientOrderID: "mlwclid2", Status: "new", Symbol: "BTCUSDT", Qty: decimal.NewFromFloat(0.1)},
		},
		positions: map[string]exchange.PositionResult{
			"BTCUSDT": {Symbol: "BTCUSDT", AvgPrice: decimal.NewFromInt(30000)},
		},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, true, 2) // pyramid = true, maxUnits = 2

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileRestored {
		t.Fatalf("want ReconcileRestored, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	if len(ids) != 1 || ids[0] != "ord2" {
		t.Fatalf("published: want [ord2], got %v", ids)
	}
}

// Scenario 14: Crash after KindOrderPlace of a pyramid add. Order filled while down.
func TestReconcile_WAL_PreFlight_PyramidAdd_Filled(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlaced("ord1", "ord1", "BTCUSDT", "buy", false),
			recFilled("ord1", "ord1", "30000"),
			recPlaceAdd("mlwclid2", "ord1", "BTCUSDT", "buy"),
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{},
		positions: map[string]exchange.PositionResult{
			"BTCUSDT": {Symbol: "BTCUSDT", AvgPrice: decimal.NewFromInt(30000)},
		},
		orderByClientOrderID: map[string]exchange.OrderResult{
			"mlwclid2": {ID: "ord2", ClientOrderID: "mlwclid2", Status: "filled", Symbol: "BTCUSDT", Qty: decimal.NewFromFloat(0.1), FilledQty: decimal.NewFromFloat(0.1), FilledAvg: decimal.NewFromInt(30500)},
		},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, true, 2)

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileFillApplied {
		t.Fatalf("want ReconcileFillApplied, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	if len(ids) != 2 || ids[0] != "ord2" || ids[1] != "ord2_filled" {
		t.Fatalf("published: want [ord2, ord2_filled], got %v", ids)
	}
}

// Scenario 15: Crash after KindOrderPlace of a pyramid add. Order never reached the exchange.
func TestReconcile_WAL_PreFlight_PyramidAdd_Cancelled(t *testing.T) {
	log := &fakePosLog{
		events: []poslog.Event{
			recPlaced("ord1", "ord1", "BTCUSDT", "buy", false),
			recFilled("ord1", "ord1", "30000"),
			recPlaceAdd("mlwclid2", "ord1", "BTCUSDT", "buy"),
		},
	}
	ex := &fakeExchange{
		openOrders: map[string]exchange.OrderResult{},
		positions: map[string]exchange.PositionResult{
			"BTCUSDT": {Symbol: "BTCUSDT", AvgPrice: decimal.NewFromInt(30000)},
		},
		orderByClientOrderID: map[string]exchange.OrderResult{},
	}
	rt := buildRuntime(ex, log)
	addHand(rt, true, 2)

	results := actor.NewReconciler(log).Reconcile(context.Background(), rt)

	if results[0].Action != actor.ReconcileCancelled {
		t.Fatalf("want ReconcileCancelled, got %s", results[0].Action)
	}
	ids := log.publishedIDs()
	if len(ids) != 1 || ids[0] != "mlwclid2_cancelled" {
		t.Fatalf("published: want [mlwclid2_cancelled], got %v", ids)
	}
}
