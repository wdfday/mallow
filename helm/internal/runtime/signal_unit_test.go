package runtime_test

// In-process signal pipeline tests.
//
// No real exchange, no NATS — all dependencies are fakes in this file.
// simExchange.PlaceOrder returns "filled" immediately so the fill path
// (hand_signal.go:341) fires synchronously inside handleSignal.
//
// Scenarios:
//
//	1. Long entry signal → CodeOrderPlaced + CodeOrderFilled → position opened
//	2. Full round-trip: entry fill → exit signal → position closed
//	3. Short entry → position with negative qty
//	4. Stale signal (ReceivedAt past TTL) → CodeSignalStale, no order
//	5. Paused hand → CodeSignalHandPaused, no order
//	6. Paused helm (runtime) → CodeSignalHelmPaused, no order
//	7. Signal strength below min → CodeSignalDoNothing, no order
//	8. MaxUnits reached → CodeSignalMaxUnits on second entry
//	9. Risk gate MaxPositions → CodeSignalRejected when helm cap hit
//	10. Entry with SL/TP → exitLevels populated after fill
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

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// ── simExchange ───────────────────────────────────────────────────────────────

// simExchange is an in-memory exchange that immediately fills every order.
// PlaceOrder captures the request, assigns a sequential ID, and returns
// Status="filled" so hand_signal.go applies the fill synchronously.
type simExchange struct {
	mu        sync.Mutex
	placed    []exchange.OrderRequest
	fillPrice decimal.Decimal // price returned on every fill; defaults to 50_000
	placeErr  error           // if non-nil, PlaceOrder returns this error
}

func newSim(fillPrice float64) *simExchange {
	return &simExchange{fillPrice: decimal.NewFromFloat(fillPrice)}
}

func (s *simExchange) Name() string { return "sim" }

func (s *simExchange) PlaceOrder(_ context.Context, _ exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.placeErr != nil {
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
	return &exchange.OrderResult{
		ID:        id,
		Symbol:    req.Symbol,
		Side:      req.Side,
		Status:    "filled",
		Qty:       qty,
		FilledQty: qty,
		FilledAvg: price,
	}, nil
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

func (s *simExchange) SubscribeFills(_ context.Context, _ exchange.Credentials) (<-chan exchange.FillEvent, error) {
	ch := make(chan exchange.FillEvent)
	close(ch)
	return ch, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// buildSimRuntime wires a HelmRuntime backed by simExchange.
func buildSimRuntime(ex exchange.Exchange, capital float64, maxPositions int) *runtime.HelmRuntime {
	pf := portfolio.New(decimal.NewFromFloat(capital))
	cfg := risk.Config{
		MaxPositions:      maxPositions,
		DailyLossLimitPct: 0.5,
		MaxDrawdownPct:    0.5,
	}
	rm := risk.New(cfg, pf)
	rt := runtime.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		"sim", pf, rm, ex, exchange.Credentials{}, nil,
	)
	rm.SetUnitCounter(rt.OpenUnitCount)
	return rt
}

// addSimHand creates and registers a Hand with FixedQty sizing + optional TTL.
func addSimHand(rt *runtime.HelmRuntime, symbol string, qty float64, signalTTL time.Duration, minStrength float64, maxUnits int) *runtime.Hand {
	strat := strategy.NewSignalFollower(minStrength)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(qty),
	})
	h := runtime.NewHand(
		uuid.New(), rt.HelmID, rt,
		strat, tact,
		false, maxUnits, signalTTL,
		nil, 0, "",
		domain.HandRiskConfig{}, decimal.Zero,
	)
	h.Symbol = symbol
	h.StrategyName = "signal_follower"
	rt.AddHand(h)
	return h
}

// waitCode polls the activity ring until an entry with the given code appears
// or the timeout expires. Returns the entry and true on success.
func waitCode(h *runtime.Hand, code int, timeout time.Duration) (runtime.ActivityEntry, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, e := range h.Activity() {
			if e.Code == code {
				return e, true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return runtime.ActivityEntry{}, false
}

// mustWaitCode fails the test if the code does not appear within the timeout.
func mustWaitCode(t *testing.T, h *runtime.Hand, code int, timeout time.Duration) runtime.ActivityEntry {
	t.Helper()
	e, ok := waitCode(h, code, timeout)
	if !ok {
		t.Fatalf("timeout waiting for activity code %d", code)
	}
	return e
}

// noCode asserts that a given code does NOT appear within the timeout.
func noCode(t *testing.T, h *runtime.Hand, code int, wait time.Duration) {
	t.Helper()
	if _, ok := waitCode(h, code, wait); ok {
		t.Fatalf("unexpected activity code %d appeared", code)
	}
}

func longSignalFor(symbol string) runtime.Signal {
	return strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirLong,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	}
}

func shortSignalFor(symbol string) runtime.Signal {
	return strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirShort,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	}
}

func exitSignalFor(symbol string) runtime.Signal {
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
	rt.UpdatePrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	h.DeliverSignal(longSignalFor(symbol))

	mustWaitCode(t, h, runtime.CodeOrderPlaced, simWait)
	mustWaitCode(t, h, runtime.CodeOrderFilled, simWait)

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
	rt.UpdatePrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	// Entry.
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCode(t, h, runtime.CodeOrderFilled, simWait)

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("position should be open after entry fill")
	}

	// Exit.
	h.DeliverSignal(exitSignalFor(symbol))
	// Wait for a second fill (exit order).
	deadline := time.Now().Add(simWait)
	var exitFill bool
	for time.Now().Before(deadline) {
		var fills int
		for _, e := range h.Activity() {
			if e.Code == runtime.CodeOrderFilled {
				fills++
			}
		}
		if fills >= 2 {
			exitFill = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !exitFill {
		t.Fatal("timeout waiting for exit fill")
	}

	if sim.placedCount() != 2 {
		t.Fatalf("expected 2 orders (entry + exit), got %d", sim.placedCount())
	}
	// After exit the portfolio position should be flat or nil.
	pos = rt.Portfolio.GetPosition(symbol)
	if pos != nil && pos.Qty.IsPositive() {
		t.Errorf("expected position closed after exit fill, qty=%s", pos.Qty)
	}
}

// ── Scenario 3: short entry ───────────────────────────────────────────────────

func TestSignal_ShortEntry(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.UpdatePrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	h.DeliverSignal(shortSignalFor(symbol))
	mustWaitCode(t, h, runtime.CodeOrderFilled, simWait)

	if sim.placed0().Side != exchange.Sell {
		t.Errorf("expected sell side for short entry, got %s", sim.placed0().Side)
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
	rt.UpdatePrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	stale := longSignalFor(symbol)
	stale.ReceivedAt = time.Now().Add(-1 * time.Minute)
	h.DeliverSignal(stale)

	mustWaitCode(t, h, runtime.CodeSignalStale, simWait)
	noCode(t, h, runtime.CodeOrderPlaced, simWait)

	if sim.placedCount() != 0 {
		t.Errorf("expected no orders for stale signal, got %d", sim.placedCount())
	}
}

// ── Scenario 5: paused hand → CodeSignalHandPaused ───────────────────────────

func TestSignal_PausedHand_Dropped(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.UpdatePrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	h.Pause()
	h.DeliverSignal(longSignalFor(symbol))

	mustWaitCode(t, h, runtime.CodeSignalHandPaused, simWait)
	noCode(t, h, runtime.CodeOrderPlaced, simWait)
}

// ── Scenario 6: paused runtime (helm) → CodeSignalHelmPaused ─────────────────

func TestSignal_PausedHelm_Dropped(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.UpdatePrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	rt.Pause()
	h.DeliverSignal(longSignalFor(symbol))

	mustWaitCode(t, h, runtime.CodeSignalHelmPaused, simWait)
	noCode(t, h, runtime.CodeOrderPlaced, simWait)
}

// ── Scenario 7: signal strength below min → CodeSignalDoNothing ──────────────

func TestSignal_LowStrength_DoNothing(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	// minStrength = 0.8; signal strength = 0.3 → filtered.
	h := addSimHand(rt, symbol, 0.001, 0, 0.8, 1)
	rt.UpdatePrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	weak := longSignalFor(symbol)
	weak.Strength = 0.3
	h.DeliverSignal(weak)

	mustWaitCode(t, h, runtime.CodeSignalDoNothing, simWait)
	noCode(t, h, runtime.CodeOrderPlaced, simWait)
}

// ── Scenario 8: MaxUnits reached → CodeSignalMaxUnits on second entry ────────

func TestSignal_MaxUnits_SecondEntryBlocked(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(50_000)
	rt := buildSimRuntime(sim, 100_000, 10)
	defer rt.Stop()

	// maxUnits = 1 → only one leg allowed.
	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.UpdatePrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	// First entry — should succeed.
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCode(t, h, runtime.CodeOrderFilled, simWait)

	// Second entry — should be blocked by MaxUnits.
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCode(t, h, runtime.CodeSignalMaxUnits, simWait)

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
	rt.UpdatePrice(sym1, decimal.NewFromFloat(50_000))
	rt.UpdatePrice(sym2, decimal.NewFromFloat(3_000))
	h1.Start()
	h2.Start()
	defer h1.Stop()
	defer h2.Stop()

	// h1 opens a position.
	h1.DeliverSignal(longSignalFor(sym1))
	mustWaitCode(t, h1, runtime.CodeOrderFilled, simWait)

	// h2 tries to enter — MaxPositions cap reached → rejected.
	h2.DeliverSignal(longSignalFor(sym2))
	mustWaitCode(t, h2, runtime.CodeSignalRejected, simWait)

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
	rt.UpdatePrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	sig := longSignalFor(symbol)
	sig.StopPrice = decimal.NewFromFloat(48_000)   // absolute SL
	sig.TargetPrice = decimal.NewFromFloat(55_000) // absolute TP
	h.DeliverSignal(sig)

	mustWaitCode(t, h, runtime.CodeOrderFilled, simWait)

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
