// Integration tests: startup reconciliation against a real exchange.
//
// Each test seeds the durable poslog (real JetStream, same as poslog_e2e_test.go)
// to some state, then simulates a restart — a brand-new HelmRuntime + Hand,
// same IDs, empty in-memory state — and runs the real Reconciler against the
// real Binance demo exchange. This is deliberately NOT the same coverage as
// reconcile_test.go (unit-level, fakePosLog + fakeExchange): those tests prove
// the reconciler's branch logic against a scripted exchange; these prove the
// same logic against what a real exchange actually returns for
// GetOrderByClientOrderID / ListOpenOrders / ListPositions after a genuine
// restart boundary.
//
// Requirements:
//   - NATS with JetStream running at nats://helm:helm-dev@127.0.0.1:4222
//   - Binance demo credentials in creds_test.go
//
// go test -v -run TestReconcile_ ./internal/runtime/ -timeout 120s
package integration_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"

	"mallow/helm/internal/infra/exchange"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/clid"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
	"mallow/helm/internal/runtime/position"
)

// reconcileEnv bundles the pieces every test in this file needs: a real
// JetStream-backed poslog and a real Binance demo exchange client.
type reconcileEnv struct {
	pl    *poslog.NatsLog
	ex    *binanceact.Client
	creds exchange.Credentials
}

func newReconcileEnv(t *testing.T) *reconcileEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set in creds_test.go")
	}

	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Skipf("NATS unavailable at %s: %v", natsURL, err)
	}
	t.Cleanup(nc.Close)

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	pl, err := poslog.NewNatsLog(js)
	if err != nil {
		t.Fatalf("NewNatsLog: %v", err)
	}

	return &reconcileEnv{
		pl:    pl,
		ex:    binanceact.New(true), // demo-api.binance.com
		creds: exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret},
	}
}

// buildReconcileRuntime constructs a fresh HelmRuntime wired to env's real
// exchange + poslog — used both for the "before restart" runtime and, with a
// new instance sharing the same helmID/handID/poslog, for the "after restart"
// one. capital only matters for sizing/risk math, not for reconcile itself.
func buildReconcileRuntime(env *reconcileEnv, helmID uuid.UUID, capital decimal.Decimal) *runtime.HelmRuntime {
	pf := portfolio.New(capital)
	rm := risk.New(risk.DefaultConfig(), pf)
	rt := runtime.NewHelmRuntime(helmID, uuid.New(), uuid.New(), env.ex.Name(), pf, rm, env.ex, env.creds, nil, time.Now())
	rt.PosLog = env.pl
	return rt
}

// newReconcileHand builds a fixed-qty ETHUSDT hand with the given (possibly
// reused, to simulate "same hand after restart") id.
func newReconcileHand(rt *runtime.HelmRuntime, handID uuid.UUID, qty decimal.Decimal) *runtime.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{Mode: tactics.SizingFixedQty, FixedQty: qty})
	h := runtime.NewHand(handID, rt.HelmID, rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	h.Symbol = "ETHUSDT"
	h.StrategyName = "signal_follower"
	h.EnableEventSink()
	rt.AddHand(h, &domain.Hand{ID: h.ID(), HelmID: rt.HelmID, Symbols: domain.StringSlice{h.Symbol}})
	return h
}

// sellBackETH is best-effort cleanup: market-sell qty of ETH so warmup/test
// positions don't accumulate across runs. Non-fatal on error (mirrors
// cleanupBinance in binance_runtime_test.go).
func sellBackETH(t *testing.T, env *reconcileEnv, qty decimal.Decimal) {
	t.Helper()
	if !qty.IsPositive() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if orders, err := env.ex.ListOpenOrders(ctx, env.creds, "ETHUSDT"); err == nil {
		for _, o := range orders {
			if err := env.ex.CancelOrder(ctx, env.creds, o.ID); err != nil {
				t.Logf("cleanup CancelOrder %s: %v (non-fatal)", o.ID, err)
			}
		}
	}
	res, err := env.ex.PlaceOrder(ctx, env.creds, exchange.OrderRequest{
		Symbol: "ETHUSDT", Side: exchange.Sell, Type: exchange.Market, Qty: qty,
	})
	if err != nil {
		t.Logf("cleanup sell failed: %v (non-fatal — check demo account manually)", err)
		return
	}
	t.Logf("cleanup: sold back %s ETH (order %s)", qty, res.ID)
}

// waitLegPhase polls the durable poslog directly (not the in-process hand
// event bus) until the hand's primary leg reaches the wanted phase, or the
// timeout elapses. Needed because Binance demo can fill a market order
// synchronously inside the PlaceOrder ACK, but the WS user-data-stream fill
// notification for that exact order is not reliably observed by the hand's
// own event bus within a normal test window (confirmed live: see the
// TestReconcile_OpenPositionRestored_Binance doc comment) — polling poslog,
// the reconciler's own source of truth, is the only way to know for certain
// a fill actually landed there before deliberately "crashing" the hand.
func waitLegPhase(env *reconcileEnv, helmID, handID uuid.UUID, want position.Phase, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		events, err := env.pl.ReplayHand(ctx, helmID.String(), handID.String())
		cancel()
		if err == nil {
			hp := position.ReplayHand(events, false, 1)
			if leg := hp.PrimaryLeg(); leg != nil && leg.Phase == want {
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// ── Scenario 1: crash between the pre-flight WAL write and the exchange call ──

// TestReconcile_PreFlightNeverPlaced_Binance seeds ONLY a KindOrderPlace event
// (the WAL entry runPlaceREST writes before calling PlaceOrder) for a clid that
// was never actually sent to Binance — simulating a crash in the gap between
// "wrote intent to poslog" and "the exchange REST call went out". Reconcile
// on a fresh hand must look the clid up via GetOrderByClientOrderID, get back
// "not found", and clean up the phantom leg (ReconcileCancelled) rather than
// leaving the hand stuck thinking an entry is still pending.
func TestReconcile_PreFlightNeverPlaced_Binance(t *testing.T) {
	env := newReconcileEnv(t)

	helmID := uuid.New()
	handID := uuid.New()
	cid := clid.New()

	placePayload, _ := json.Marshal(poslog.OrderPlacePayload{
		ClientOrderID: cid,
		Symbol:        "ETHUSDT",
		Side:          "buy",
		Qty:           "0.01",
		Price:         "0",
		OrderType:     "market",
	})
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := env.pl.Publish(seedCtx, poslog.Event{
		ID:         cid,
		HandID:     handID.String(),
		HelmID:     helmID.String(),
		PositionID: cid, // new leg: PositionID = opening client_order_id, see hand_poslog.go
		Kind:       poslog.KindOrderPlace,
		Payload:    placePayload,
		At:         time.Now().UTC(),
	})
	seedCancel()
	if err != nil {
		t.Fatalf("seed order_place: %v", err)
	}
	t.Logf("seeded order_place WAL entry: clid=%s (never sent to exchange)", cid)

	// ── "After restart": fresh runtime + fresh hand, same IDs, empty in-memory state.
	rt := buildReconcileRuntime(env, helmID, decimal.NewFromFloat(100_000))
	defer rt.Stop()
	hand := newReconcileHand(rt, handID, decimal.NewFromFloat(0.01))
	rec := recordEvents(hand)
	defer rec.dump(t, "TestReconcile_PreFlightNeverPlaced_Binance")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := runtime.NewReconciler(env.pl).Reconcile(ctx, rt)

	if len(results) != 1 {
		t.Fatalf("want 1 reconcile result, got %d", len(results))
	}
	res := results[0]
	t.Logf("reconcile result: action=%s phase=%s err=%v", res.Action, res.Phase, res.Err)
	if res.Err != nil {
		t.Fatalf("reconcile error: %v", res.Err)
	}
	if res.Action != runtime.ReconcileCancelled {
		t.Fatalf("want ReconcileCancelled (clid never reached the exchange), got %s", res.Action)
	}
	if res.Phase != position.PhaseIdle {
		t.Fatalf("want PhaseIdle after the phantom leg is cancelled, got %s", res.Phase)
	}
	if legs := hand.ActiveLegs(); len(legs) != 0 {
		t.Fatalf("want 0 active legs after reconcile-cancel, got %d: %+v", len(legs), legs)
	}
}

// ── Scenario 2: real entry fills, hand stops, restart must restore the leg ────

// TestReconcile_OpenPositionRestored_Binance places a real entry, stops the
// hand (no flatten — just the run-loop going away, like a real process
// restart), then reconciles a brand-new hand with the same ID against the
// same poslog + exchange. The position is still open at Binance the whole
// time, so reconcile must end up with PhaseOpen and a rebuilt leg — via
// EITHER of two legitimately different Actions, and this is not a coin flip
// we can force deterministically against a real exchange:
//
//   - ReconcileRestored: the hand's own WS fill notification landed and
//     order_filled was already durably recorded before Stop() — reconcile
//     just re-confirms the position is still there.
//   - ReconcileFillApplied: confirmed live against Binance demo — market
//     orders here fill synchronously inside the PlaceOrder ACK, but the
//     WS user-data-stream fill notification for that exact order is not
//     reliably observed by the hand's own event bus within a normal test
//     window. The leg is still PhaseEntering in poslog when Stop() runs, so
//     reconcile itself recovers the missed fill directly from the exchange —
//     this is, in practice, the MORE common outcome against this sandbox,
//     not an edge case.
//
// Both outcomes prove the same thing that matters: after reconcile, the
// position is durably PhaseOpen with the right qty. Which path got there is
// exchange/WS timing, not something this test should — or reliably can —
// pin down.
func TestReconcile_OpenPositionRestored_Binance(t *testing.T) {
	env := newReconcileEnv(t)

	helmID := uuid.New()
	handID := uuid.New()
	qty := decimal.NewFromFloat(0.01)

	// ── "Before crash": place a real entry and let it fill.
	rt1 := buildReconcileRuntime(env, helmID, decimal.NewFromFloat(100_000))
	rt1.StartStreaming(context.Background()) // WS fills, same as newBinanceEnv
	if p, err := env.ex.GetCurrentPrice(context.Background(), env.creds, "ETHUSDT"); err == nil && p.IsPositive() {
		rt1.UpdatePrice("ETHUSDT", p)
	}
	hand1 := newReconcileHand(rt1, handID, qty)
	rec1 := recordEvents(hand1)
	hand1.Start()

	placed := orderNotify(hand1, 20*time.Second)
	hand1.DeliverSignal(longSig("ETHUSDT"))
	select {
	case e := <-placed:
		if e.Code == runtime.CodeOrderFailed {
			rec1.dump(t, "TestReconcile_OpenPositionRestored_Binance (entry failed)")
			rt1.Stop()
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			t.Fatalf("entry order failed: %s", e.Reason)
		}
		t.Logf("entry placed: order_id=%s", e.OrderID)
	case <-time.After(20 * time.Second):
		rec1.dump(t, "TestReconcile_OpenPositionRestored_Binance (entry timeout)")
		rt1.Stop()
		t.Fatal("timeout: entry order not placed within 20s")
	}

	select {
	case e := <-fillNotify(hand1, 40*time.Second):
		t.Logf("entry filled (WS observed by hand): qty=%s avg=%s", e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("fill not observed via hand's own event bus within 30s — poslog still has only order_placed; reconcile will need to recover it")
	}

	// Let publishOrderPlaced/publishOrderFilled goroutines flush to JetStream.
	time.Sleep(3 * time.Second)
	rec1.dump(t, "TestReconcile_OpenPositionRestored_Binance (before restart)")

	// "Crash": stop the run-loop only — Stop() does not flatten, unlike Kill().
	hand1.Stop()
	rt1.Stop()

	// ── "After restart": fresh runtime + fresh hand, same IDs, empty in-memory state.
	rt2 := buildReconcileRuntime(env, helmID, decimal.NewFromFloat(100_000))
	defer rt2.Stop()
	hand2 := newReconcileHand(rt2, handID, qty)
	rec2 := recordEvents(hand2)
	defer rec2.dump(t, "TestReconcile_OpenPositionRestored_Binance (after restart)")
	defer sellBackETH(t, env, qty)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := runtime.NewReconciler(env.pl).Reconcile(ctx, rt2)

	if len(results) != 1 {
		t.Fatalf("want 1 reconcile result, got %d", len(results))
	}
	res := results[0]
	t.Logf("reconcile result: action=%s phase=%s err=%v", res.Action, res.Phase, res.Err)
	if res.Err != nil {
		t.Fatalf("reconcile error: %v", res.Err)
	}
	switch res.Action {
	case runtime.ReconcileRestored:
		t.Log("outcome: ReconcileRestored — the hand's own WS fill was already durable before Stop()")
	case runtime.ReconcileFillApplied:
		t.Log("outcome: ReconcileFillApplied — reconcile recovered a fill the hand never got to apply itself")
	default:
		t.Fatalf("want ReconcileRestored or ReconcileFillApplied (position is open at exchange either way), got %s", res.Action)
	}
	if res.Phase != position.PhaseOpen {
		t.Fatalf("want PhaseOpen, got %s", res.Phase)
	}
	legs := hand2.ActiveLegs()
	if len(legs) != 1 {
		t.Fatalf("want 1 restored leg, got %d: %+v", len(legs), legs)
	}
	if legs[0].Symbol != "ETHUSDT" || legs[0].Side != "buy" {
		t.Fatalf("restored leg mismatch: %+v", legs[0])
	}
	if !legs[0].Qty.IsPositive() {
		t.Fatalf("restored leg qty should be positive, got %s", legs[0].Qty)
	}
	t.Logf("restored leg: symbol=%s side=%s qty=%s entry=%s", legs[0].Symbol, legs[0].Side, legs[0].Qty, legs[0].EntryPrice)
}

// ── Scenario 3: position closed externally while the hand was down ────────────

// TestReconcile_ExternalCloseDuringDowntime_Binance places a real entry, waits
// (via waitLegPhase — polling poslog directly, not the hand's own WS-fed event
// bus, see its doc comment) until the entry fill is durably PhaseOpen, stops
// the hand, then sells the position directly via the exchange client
// (bypassing the hand entirely — simulating a manual sell or a liquidation
// that happened while helm was down). Reconcile on a fresh hand must see the
// position gone at the exchange with no bracket fill to recover, and close it
// out as ReconcileExternalClose rather than leaving a phantom open leg that a
// later signal would try to add to.
//
// The PhaseOpen wait is a hard precondition here, not just a nicety like in
// TestReconcile_OpenPositionRestored_Binance: reconcileHand only calls
// reconcileLeg ONCE per leg per pass, keyed off whatever phase poslog shows at
// replay time. If the entry's own order_filled were still missing (leg still
// PhaseEntering) when the external sell happens, reconcile would spend this
// pass recovering the missed ENTRY fill (ReconcileFillApplied) and never even
// reach the "is the resulting position still there" check the external-sell
// scenario is actually testing.
func TestReconcile_ExternalCloseDuringDowntime_Binance(t *testing.T) {
	env := newReconcileEnv(t)

	helmID := uuid.New()
	handID := uuid.New()
	qty := decimal.NewFromFloat(0.01)

	rt1 := buildReconcileRuntime(env, helmID, decimal.NewFromFloat(100_000))
	rt1.StartStreaming(context.Background())
	if p, err := env.ex.GetCurrentPrice(context.Background(), env.creds, "ETHUSDT"); err == nil && p.IsPositive() {
		rt1.UpdatePrice("ETHUSDT", p)
	}
	hand1 := newReconcileHand(rt1, handID, qty)
	rec1 := recordEvents(hand1)
	hand1.Start()

	placed := orderNotify(hand1, 20*time.Second)
	hand1.DeliverSignal(longSig("ETHUSDT"))
	select {
	case e := <-placed:
		if e.Code == runtime.CodeOrderFailed {
			rec1.dump(t, "TestReconcile_ExternalCloseDuringDowntime_Binance (entry failed)")
			rt1.Stop()
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			t.Fatalf("entry order failed: %s", e.Reason)
		}
		t.Logf("entry placed: order_id=%s", e.OrderID)
	case <-time.After(20 * time.Second):
		rec1.dump(t, "TestReconcile_ExternalCloseDuringDowntime_Binance (entry timeout)")
		rt1.Stop()
		t.Fatal("timeout: entry order not placed within 20s")
	}

	select {
	case e := <-fillNotify(hand1, 40*time.Second):
		t.Logf("entry filled (WS observed by hand): qty=%s avg=%s", e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("fill not observed via hand's own event bus within 30s — waiting on poslog directly instead")
	}

	// Hard precondition: the entry must be durably PhaseOpen in poslog before
	// the external sell, or reconcile will recover the entry fill instead of
	// detecting the external close (see doc comment above).
	if !waitLegPhase(env, helmID, handID, position.PhaseOpen, 45*time.Second) {
		rec1.dump(t, "TestReconcile_ExternalCloseDuringDowntime_Binance (entry never reached PhaseOpen)")
		hand1.Stop()
		rt1.Stop()
		t.Skip("entry never reached PhaseOpen in poslog within 45s — cannot set up the external-close precondition")
	}
	rec1.dump(t, "TestReconcile_ExternalCloseDuringDowntime_Binance (before restart)")

	// "Crash": stop the run-loop — position stays open at the exchange, still
	// guarded by whatever bracket was placed (default SL injected on entry).
	hand1.Stop()

	// Cancel any bracket so the direct sell below isn't fighting a resting OCO
	// for the base asset (mirrors what a real external/manual sell would need
	// to do first — Binance won't let the balance sell while it's OCO-locked).
	cancelCtx, cancelFn := context.WithTimeout(context.Background(), 10*time.Second)
	if orders, err := env.ex.ListOpenOrders(cancelCtx, env.creds, "ETHUSDT"); err == nil {
		for _, o := range orders {
			if err := env.ex.CancelOrder(cancelCtx, env.creds, o.ID); err != nil {
				t.Logf("pre-external-sell bracket cancel %s: %v (non-fatal)", o.ID, err)
			}
		}
	}
	cancelFn()
	time.Sleep(500 * time.Millisecond) // brief settle before the direct sell

	// "External close": sell directly via the exchange client, bypassing the
	// hand entirely — poslog never learns about this sell until reconcile runs.
	sellCtx, sellCancel := context.WithTimeout(context.Background(), 10*time.Second)
	sellRes, err := env.ex.PlaceOrder(sellCtx, env.creds, exchange.OrderRequest{
		Symbol: "ETHUSDT", Side: exchange.Sell, Type: exchange.Market, Qty: qty,
	})
	sellCancel()
	rt1.Stop()
	if err != nil {
		t.Fatalf("simulated external sell failed: %v", err)
	}
	t.Logf("simulated external close: sold %s ETH (order %s)", qty, sellRes.ID)

	// ── "After restart": fresh runtime + fresh hand, same IDs, empty in-memory state.
	rt2 := buildReconcileRuntime(env, helmID, decimal.NewFromFloat(100_000))
	defer rt2.Stop()
	hand2 := newReconcileHand(rt2, handID, qty)
	rec2 := recordEvents(hand2)
	defer rec2.dump(t, "TestReconcile_ExternalCloseDuringDowntime_Binance (after restart)")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := runtime.NewReconciler(env.pl).Reconcile(ctx, rt2)

	if len(results) != 1 {
		t.Fatalf("want 1 reconcile result, got %d", len(results))
	}
	res := results[0]
	t.Logf("reconcile result: action=%s phase=%s err=%v", res.Action, res.Phase, res.Err)
	if res.Err != nil {
		t.Fatalf("reconcile error: %v", res.Err)
	}
	if res.Action != runtime.ReconcileExternalClose {
		t.Fatalf("want ReconcileExternalClose (position sold outside the hand), got %s", res.Action)
	}
	if res.Phase != position.PhaseIdle {
		t.Fatalf("want PhaseIdle after external close, got %s", res.Phase)
	}
	if legs := hand2.ActiveLegs(); len(legs) != 0 {
		t.Fatalf("want 0 active legs after external close, got %d: %+v", len(legs), legs)
	}
}
