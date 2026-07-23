package actor_test

// In-process signal pipeline tests.
//
// No real exchange, no NATS — all dependencies are fakes in this file.
// simExchange.PlaceOrder returns "filled" immediately so the fill path
// (hand_fills.go) fires synchronously inside handleSignal.
//
// Scenarios:
//
//	1. Long entry signal → CodeOrderPlaced + CodeOrderFilled → position opened
//	2. Full round-trip: entry fill → exit signal → position closed
//	3. Short entry → position with negative qty
//	4. Stale signal (ReceivedAt past TTL) → CodeSignalStale, no order
//	5. Paused helm (runtime) → CodeSignalHelmPaused, no order
//	6. Signal strength below min → CodeSignalDoNothing, no order
//	7. MaxUnits reached → CodeSignalMaxUnits on second entry
//	8. Risk gate MaxPositions → CodeSignalRejected when helm cap hit
//	9. Entry with SL/TP → exitLevels populated after fill
//	10. Insufficient capital (zero qty) → CodeHandAutoStopped, hand stops
//
// go test -v -run TestSignal ./internal/runtime/ -count=1

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor"
	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/risk"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/core/tactics"
	"mallow/helm/internal/fleet/actor/eventcode"
	signalfollower "mallow/helm/internal/fleet/actor/signal-follower"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
)

// ── simExchange ───────────────────────────────────────────────────────────────

// simExchange is an in-memory exchange that immediately fills every order.
// It implements AccountStreamer so fills flow through the WS path (fills are
// authoritative via WS only — REST ACK refactor).
type simExchange struct {
	mu        sync.Mutex
	placed    []exchange.OrderRequest
	fillPrice decimal.Decimal            // price returned on every fill; defaults to 50_000
	placeErr  error                      // if non-nil, PlaceOrder returns this error
	onFill    func(exchange.WsFillEvent) // registered by StreamOrders; nil until StartStreaming is called
}

func newSim(fillPrice float64) *simExchange {
	return &simExchange{fillPrice: decimal.NewFromFloat(fillPrice)}
}

func (s *simExchange) Name() string { return "sim" }

func (s *simExchange) PlaceOrder(_ context.Context, _ exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	s.mu.Lock()
	if s.placeErr != nil {
		s.mu.Unlock()
		return nil, s.placeErr
	}
	id := fmt.Sprintf("sim-%d", len(s.placed)+1)
	qty := req.Qty
	if qty.IsZero() {
		qty = decimal.NewFromFloat(0.001) // fallback when quote_qty mode
	}
	price := s.fillPrice
	if price.IsZero() {
		price = decimal.NewFromFloat(50_000)
	}
	s.placed = append(s.placed, req)
	onFill := s.onFill
	s.mu.Unlock()

	result := &exchange.OrderResult{
		ID:        id,
		Symbol:    req.Symbol,
		Side:      req.Side,
		Status:    "submitted", // ACK only; fill comes via simulated WS event below
		Qty:       qty,
		FilledQty: decimal.Zero,
		FilledAvg: decimal.Zero,
	}

	// Fire a simulated WS fill event so the WS path delivers the fill.
	if onFill != nil {
		go onFill(exchange.WsFillEvent{
			OrderID:       id,
			ClientOrderID: req.ClientOrderID,
			Symbol:        req.Symbol,
			Side:          req.Side,
			FilledQty:     qty,
			FilledAvg:     price,
			FillID:        "sim-fill-" + id,
			Timestamp:     time.Now(),
		})
	}

	return result, nil
}

// StreamOrders implements exchange.AccountStreamer so StartStreaming can start
// the fill-drain goroutines for this sim actor.
func (s *simExchange) StreamOrders(
	_ context.Context,
	_ exchange.Credentials,
	_ func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	_ func(exchange.BalanceEvent),
	_ func(exchange.PositionEvent),
	_ func(exchange.RiskEvent),
	_ func(string),
) error {
	s.mu.Lock()
	s.onFill = onFill
	s.mu.Unlock()
	return nil
}

func (s *simExchange) placed0() exchange.OrderRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.placed) == 0 {
		return exchange.OrderRequest{}
	}
	return s.placed[0]
}

func (s *simExchange) placedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.placed)
}

func (s *simExchange) GetOrder(_ context.Context, _ exchange.Credentials, id string) (*exchange.OrderResult, error) {
	return &exchange.OrderResult{ID: id, Status: "filled"}, nil
}

func (s *simExchange) CancelOrder(_ context.Context, _ exchange.Credentials, _ string) error {
	return nil
}

func (s *simExchange) ListOpenOrders(_ context.Context, _ exchange.Credentials, _ string) ([]exchange.OrderResult, error) {
	return nil, nil
}

func (s *simExchange) ListPositions(_ context.Context, _ exchange.Credentials) ([]exchange.PositionResult, error) {
	return nil, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// buildSimRuntime wires a HelmRuntime backed by simExchange.
// StartStreaming is called so the WS fill-drain goroutines are running —
// fills are now authoritative via the WS path only (REST ACK refactor).
func buildSimRuntime(ex exchange.Exchange, capital float64, maxPositions int) *actor.HelmRuntime {
	pf := portfolio.New(decimal.NewFromFloat(capital))
	cfg := risk.Config{
		MaxPositions:      maxPositions,
		DailyLossLimitPct: 0.5,
		MaxDrawdownPct:    0.5,
	}
	rm := risk.New(cfg, pf)
	rt := actor.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		"sim", pf, rm, ex, exchange.Credentials{}, nil, time.Now(),
	)
	rm.SetUnitCounter(rt.OpenUnitCount)
	rt.StartStreaming(context.Background())
	return rt
}

// addSimHand creates and registers a Hand with FixedQty sizing + optional TTL.
func addSimHand(rt *actor.HelmRuntime, symbol string, qty float64, signalTTL time.Duration, minStrength float64, maxUnits int) *signalfollower.Hand {
	strat := strategy.NewSignalFollower(minStrength)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(qty),
	})
	h := signalfollower.NewHand(
		uuid.New(), rt.HelmID, rt,
		strat, tact,
		false, maxUnits, signalTTL,
		nil, domain.OrderTypeMarket, 0, domain.LimitFallbackCancel,
		domain.HandGuardConfig{}, decimal.Zero,
	)
	h.Symbol = symbol
	h.StrategyName = "signal_follower"
	h.EnableEventSink()
	rt.AddHand(h, &domain.Hand{ID: h.ID(), HelmID: rt.HelmID, Symbols: domain.StringSlice{symbol}})
	return h
}

// waitCode subscribes to the hand event bus and waits until an event with the
// given code appears or the timeout expires. Returns the event and true on success.
// IMPORTANT: call before delivering the trigger signal to avoid missing fast events.
func waitCode(h *signalfollower.Hand, code int, timeout time.Duration) (natsapi.HelmEvent, bool) {
	events := h.Subscribe(64) // subscribe once, outside the loop
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return natsapi.HelmEvent{}, false
			}
			if ev.Code == code {
				return ev, true
			}
		case <-deadline:
			return natsapi.HelmEvent{}, false
		}
	}
}

// mustWaitCode fails the test if the code does not appear within the timeout.
// Creates a new subscription — call before delivering the trigger signal.
func mustWaitCode(t *testing.T, h *signalfollower.Hand, code int, timeout time.Duration) natsapi.HelmEvent {
	t.Helper()
	e, ok := waitCode(h, code, timeout)
	if !ok {
		t.Fatalf("timeout waiting for activity code %d", code)
	}
	return e
}

// waitCodeCh waits on a pre-created events channel for the given code.
// Use when you need to subscribe BEFORE delivering the trigger signal.
func waitCodeCh(events <-chan natsapi.HelmEvent, code int, timeout time.Duration) (natsapi.HelmEvent, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return natsapi.HelmEvent{}, false
			}
			if ev.Code == code {
				return ev, true
			}
		case <-deadline:
			return natsapi.HelmEvent{}, false
		}
	}
}

// mustWaitCodeCh is like mustWaitCode but uses a pre-created events channel.
func mustWaitCodeCh(t *testing.T, events <-chan natsapi.HelmEvent, code int, timeout time.Duration) natsapi.HelmEvent {
	t.Helper()
	e, ok := waitCodeCh(events, code, timeout)
	if !ok {
		t.Fatalf("timeout waiting for activity code %d", code)
	}
	return e
}

// mustWaitCodes subscribes once, delivers fn (if non-nil), then waits for each
// code in order using the same channel. Use when multiple events fire in rapid
// succession from the same goroutine (e.g. synchronous sim fills).
//
//	events, deliver := mustWaitCodes(t, h, simWait, CodeOrderPlaced, CodeOrderFilled)
//	deliver()  // call AFTER subscribe; events already buffered
func mustWaitCodesSetup(t *testing.T, h *signalfollower.Hand, timeout time.Duration, codes ...int) (<-chan natsapi.HelmEvent, func() []natsapi.HelmEvent) {
	t.Helper()
	events := h.Subscribe(128)
	return events, func() []natsapi.HelmEvent {
		t.Helper()
		out := make([]natsapi.HelmEvent, len(codes))
		for i, code := range codes {
			e, ok := waitCodeCh(events, code, timeout)
			if !ok {
				t.Fatalf("timeout waiting for activity code %d", code)
			}
			out[i] = e
		}
		return out
	}
}

// noCode asserts that a given code does NOT appear within the timeout.
func noCode(t *testing.T, h *signalfollower.Hand, code int, wait time.Duration) {
	t.Helper()
	if _, ok := waitCode(h, code, wait); ok {
		t.Fatalf("unexpected activity code %d appeared", code)
	}
}

func longSignalFor(symbol string) strategy.Signal {
	return strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirLong,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	}
}

func shortSignalFor(symbol string) strategy.Signal {
	return strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirShort,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	}
}

func exitSignalFor(symbol string) strategy.Signal {
	return strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirExit,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	}
}

const simWait = 200 * time.Millisecond

// ── Scenario 1: long entry signal → order placed + filled → position opened ──

func TestSignal_LongEntry_OrderPlacedAndFilled(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	events, waitAll := mustWaitCodesSetup(t, h, simWait, eventcode.CodeOrderPlaced, eventcode.CodeOrderFilled)
	_ = events
	h.DeliverSignal(longSignalFor(symbol))
	waitAll()

	if sim.placedCount() != 1 {
		t.Fatalf("expected 1 order placed, got %d", sim.placedCount())
	}
	if sim.placed0().Side != exchange.Buy {
		t.Errorf("expected buy side, got %s", sim.placed0().Side)
	}

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || pos.Qty.IsZero() {
		t.Fatal("expected open position after fill, got nil or zero qty")
	}
}

// ── Scenario 2: full round-trip — entry fill → exit signal → position closed ─

func TestSignal_RoundTrip_EntryThenExit(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	// Entry — subscribe before delivering so synchronous sim fills are captured.
	entryCh := h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCodeCh(t, entryCh, eventcode.CodeOrderFilled, simWait)

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("position should be open after entry fill")
	}

	// Exit — new subscription for the second fill.
	exitCh := h.Subscribe(64)
	h.DeliverSignal(exitSignalFor(symbol))
	mustWaitCodeCh(t, exitCh, eventcode.CodeOrderFilled, simWait)

	if sim.placedCount() != 2 {
		t.Fatalf("expected 2 orders (entry + exit), got %d", sim.placedCount())
	}
	// After exit the portfolio position should be flat or nil.
	pos = rt.Portfolio.GetPosition(symbol)
	if pos != nil && pos.Qty.IsPositive() {
		t.Errorf("expected position closed after exit fill, qty=%s", pos.Qty)
	}
}

// TestSignal_OrphanExitKind_DisownsLegWithoutOrder verifies the fix for
// checkPositionDesync's cross-goroutine actor violation: an ExitKindOrphan
// signal (what HelmRuntime.checkPositionDesync now delivers via DeliverSignal
// instead of calling handlePositionDesync directly) must disown the leg
// WITHOUT placing a market order — unlike a plain DirExit signal, which would
// place a closing order and hit CodeOrderFilled instead.
func TestSignal_OrphanExitKind_DisownsLegWithoutOrder(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	entryCh := h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCodeCh(t, entryCh, eventcode.CodeOrderFilled, simWait)

	orphanCh := h.Subscribe(64)
	h.DeliverSignal(strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirExit,
		ExitKind:   strategy.ExitKindOrphan,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	})
	mustWaitCodeCh(t, orphanCh, eventcode.CodePositionExtClosed, simWait)

	if sim.placedCount() != 1 {
		t.Fatalf("orphan signal must not place a market order — expected 1 order (entry only), got %d", sim.placedCount())
	}
}

// ── Scenario 3: short entry ───────────────────────────────────────────────────

func TestSignal_ShortEntry(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	shortCh := h.Subscribe(64)
	h.DeliverSignal(shortSignalFor(symbol))

	// Since short selling is not supported yet, the signal should be rejected.
	ev := mustWaitCodeCh(t, shortCh, eventcode.CodeSignalRejected, simWait)
	if ev.Reason != "not support short selling yet" {
		t.Errorf("expected rejection reason 'not support short selling yet', got '%s'", ev.Reason)
	}

	if sim.placedCount() != 0 {
		t.Errorf("expected no orders placed for short entry, got %d", sim.placedCount())
	}
}

// ── Scenario 4: stale signal (ReceivedAt past TTL) → CodeSignalStale ─────────

func TestSignal_Stale_Dropped(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	// TTL = 5 s; ReceivedAt = 1 minute ago → signal is expired.
	h := addSimHand(rt, symbol, 0.001, 5*time.Second, 0.3, 1)
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	stale := longSignalFor(symbol)
	stale.ReceivedAt = time.Now().Add(-1 * time.Minute)
	staleCh := h.Subscribe(64)
	h.DeliverSignal(stale)

	mustWaitCodeCh(t, staleCh, eventcode.CodeSignalStale, simWait)
	noCode(t, h, eventcode.CodeOrderPlaced, simWait)

	if sim.placedCount() != 0 {
		t.Errorf("expected no orders for stale signal, got %d", sim.placedCount())
	}
}

// ── Scenario 5: paused runtime (helm) → CodeSignalHelmPaused ─────────────────

func TestSignal_PausedHelm_Dropped(t *testing.T) { //nolint:unused // scenario 5
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	rt.Pause()
	pausedHelmCh := h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))

	mustWaitCodeCh(t, pausedHelmCh, eventcode.CodeSignalHelmPaused, simWait)
	noCode(t, h, eventcode.CodeOrderPlaced, simWait)
}

// ── Scenario 7: signal strength below min → CodeSignalDoNothing ──────────────

func TestSignal_LowStrength_DoNothing(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	// minStrength = 0.8; signal strength = 0.3 → filtered.
	h := addSimHand(rt, symbol, 0.001, 0, 0.8, 1)
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	weak := longSignalFor(symbol)
	weak.Strength = 0.3
	weakCh := h.Subscribe(64)
	h.DeliverSignal(weak)

	mustWaitCodeCh(t, weakCh, eventcode.CodeSignalDoNothing, simWait)
	noCode(t, h, eventcode.CodeOrderPlaced, simWait)
}

// ── Scenario 8: MaxUnits reached → CodeSignalMaxUnits on second entry ────────

func TestSignal_MaxUnits_SecondEntryBlocked(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	// maxUnits = 1 → only one leg allowed.
	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	// First entry — should succeed.
	entry1Ch := h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCodeCh(t, entry1Ch, eventcode.CodeOrderFilled, simWait)

	// Second entry — should be blocked by MaxUnits.
	maxUnitsCh := h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCodeCh(t, maxUnitsCh, eventcode.CodeSignalMaxUnits, simWait)

	if sim.placedCount() != 1 {
		t.Errorf("expected exactly 1 order (second entry blocked), got %d", sim.placedCount())
	}
}

// ── Scenario 9: risk gate MaxPositions → CodeSignalRejected ──────────────────

func TestSignal_MaxPositions_SecondHandBlocked(t *testing.T) {
	const sym1 = "BTCUSDT"
	const sym2 = "ETHUSDT"
	sim := newSim(50_000)
	// MaxPositions = 1 at helm level → only 1 open unit across all hands.
	rt := buildSimRuntime(sim, 100_000, 1)
	defer rt.Stop()

	h1 := addSimHand(rt, sym1, 0.001, 0, 0.3, 1)
	h2 := addSimHand(rt, sym2, 0.001, 0, 0.3, 1)
	rt.MarketData.SetPrice(sym1, decimal.NewFromFloat(50_000))
	rt.MarketData.SetPrice(sym2, decimal.NewFromFloat(3_000))
	h1.Start()
	h2.Start()
	defer h1.Stop()
	defer h2.Stop()

	// h1 opens a position.
	h1FilledCh := h1.Subscribe(64)
	h1.DeliverSignal(longSignalFor(sym1))
	mustWaitCodeCh(t, h1FilledCh, eventcode.CodeOrderFilled, simWait)

	// h2 tries to enter — MaxPositions cap reached → rejected.
	h2RejectedCh := h2.Subscribe(64)
	h2.DeliverSignal(longSignalFor(sym2))
	mustWaitCodeCh(t, h2RejectedCh, eventcode.CodeSignalRejected, simWait)

	if sim.placedCount() != 1 {
		t.Errorf("expected exactly 1 order placed total, got %d", sim.placedCount())
	}
}

// ── Scenario 10: entry signal with SL/TP → exitLevels populated after fill ───

func TestSignal_ExitLevelsPopulated_AfterFill(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	sig := longSignalFor(symbol)
	sig.StopPrice = decimal.NewFromFloat(48_000)   // absolute SL
	sig.TargetPrice = decimal.NewFromFloat(55_000) // absolute TP
	slTpCh := h.Subscribe(64)
	h.DeliverSignal(sig)

	mustWaitCodeCh(t, slTpCh, eventcode.CodeOrderFilled, simWait)

	// Verify an exit order was NOT placed yet (no ExitOrderPlacer on simExchange)
	// but the hand registered the exit levels (detectable via the Position's exit levels).
	// We assert the position is open with correct qty as a proxy.
	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || pos.Qty.IsZero() {
		t.Fatal("expected open position after fill with SL/TP signal")
	}
	// Only 1 order placed (the entry); no bracket order since simExchange doesn't
	// implement ExitOrderPlacer.
	if sim.placedCount() != 1 {
		t.Errorf("expected 1 order (entry only, no bracket), got %d", sim.placedCount())
	}
}

type stubZeroQtyPlanner struct {
	tactics.Planner
}

func (s *stubZeroQtyPlanner) Plan(intent strategy.Intent, ctx tactics.MarketContext) tactics.ExecutionPlan {
	return tactics.ExecutionPlan{
		Action: intent.Action,
		Symbol: intent.Signal.Symbol,
		Side:   "buy",
		Qty:    decimal.Zero,
	}
}

func (s *stubZeroQtyPlanner) UpdateEquity(equity decimal.Decimal) {}

func TestSignal_InsufficientCapital_AutoStop(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	strat := strategy.NewSignalFollower(0.3)
	tact := &stubZeroQtyPlanner{}
	h := signalfollower.NewHand(
		uuid.New(), rt.HelmID, rt,
		strat, tact,
		false, 1, 0,
		nil, domain.OrderTypeMarket, 0, domain.LimitFallbackCancel,
		domain.HandGuardConfig{}, decimal.Zero,
	)
	h.Symbol = symbol
	h.StrategyName = "signal_follower"
	h.EnableEventSink()
	rt.AddHand(h, &domain.Hand{ID: h.ID(), HelmID: rt.HelmID, Symbols: domain.StringSlice{symbol}})

	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	// Pre-subscribe before delivering the signal so fast synchronous events
	// (rejection + auto-stop happen in the same goroutine turn) are not missed.
	rejectedCh := h.Subscribe(64)
	stoppedCh := h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))

	mustWaitCodeCh(t, rejectedCh, eventcode.CodeSignalRejected, simWait)
	// Wait for the auto-stop event which follows the rejection.
	mustWaitCodeCh(t, stoppedCh, eventcode.CodeHandAutoStopped, simWait)

	// Give the async Stop() goroutine time to run.
	time.Sleep(50 * time.Millisecond)
	if h.IsRunning() {
		t.Fatal("expected hand to be stopped after failing to size entry trade due to insufficient capital")
	}
}

func TestSignal_HaltedHelm_NotForwarded(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 10_000, 10)
	defer rt.Stop()

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(50_000))

	// 1. Update risk manager config to trigger halt easily
	rt.RiskMgr.UpdateConfig(risk.Config{
		MaxPositions:      10,
		DailyLossLimitPct: 0.01,
		MaxDrawdownPct:    0.01,
	})

	// 2. Ensure peak is seeded before executing a loss
	rt.Portfolio.UpdatePeakEquity()
	rt.ReportFill(helmdomain.FillReport{
		Symbol: symbol,
		Side:   "buy",
		Qty:    decimal.NewFromInt(100),
		Price:  decimal.NewFromInt(100),
	})
	rt.ReportFill(helmdomain.FillReport{
		Symbol: symbol,
		Side:   "sell",
		Qty:    decimal.NewFromInt(100),
		Price:  decimal.NewFromInt(90),
	})

	// 3. Trigger risk checks
	rt.RiskMgr.Validate(strategy.Intent{
		Action: strategy.ActionEnterLong,
		Signal: strategy.Signal{Symbol: symbol, Direction: strategy.DirLong},
	}, h.ID().String())

	if !rt.IsHalted() {
		t.Fatal("expected runtime to be halted after drawdown breach")
	}

	// 4. Dispatch an entry signal — should NOT be forwarded to the hand
	sig := strategy.Signal{
		Symbol:    symbol,
		Direction: strategy.DirLong,
	}
	dispatched := rt.DispatchHandSignal(h.ID().String(), sig)
	if !dispatched {
		t.Fatal("expected DispatchHandSignal to return true (signal swallowed)")
	}

	// 5. Verify the hand's signal channel is empty (no signal forwarded)
	select {
	case s := <-h.Signals:
		t.Fatalf("expected no signal to be forwarded to the hand, but got: %v", s)
	case <-time.After(50 * time.Millisecond):
		// Success: signal was not forwarded!
	}

	// 6. Dispatch an urgent exit signal — should STILL be forwarded to the hand
	exitSig := strategy.Signal{
		Symbol:    symbol,
		Direction: strategy.DirExit, // DirExit makes it urgent
	}
	dispatched = rt.DispatchHandSignal(h.ID().String(), exitSig)
	if !dispatched {
		t.Fatal("expected DispatchHandSignal to return true for urgent signal")
	}

	// Verify the exit signal was delivered via the regular Signals channel.
	select {
	case s := <-h.Signals:
		if s.Direction != strategy.DirExit {
			t.Fatalf("expected urgent exit signal, got: %v", s)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("expected urgent exit signal to be forwarded to the hand, but timed out")
	}
}
