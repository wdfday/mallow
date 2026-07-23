package signalfollower_test

// Shared in-process sim-exchange test harness for signalfollower's external
// tests (hand_guard_test.go). Mirrors the harness in
// mallow/helm/internal/fleet/actor/signal_unit_test.go (package actor_test) —
// duplicated rather than shared because Go test packages can't export
// unexported test-only helpers across package boundaries, and actor_test's
// harness is itself scoped to *actor.Hand / actor.Signal, which no longer
// exist now that Hand lives in this package.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/google/uuid"

	"mallow/helm/internal/fleet/actor"
	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/risk"
	"mallow/helm/internal/fleet/actor/core/strategy"
	signalfollower "mallow/helm/internal/fleet/actor/signal-follower"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
)

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

// mustWaitCodeCh is like waitCode but uses a pre-created events channel and
// fails the test on timeout.
func mustWaitCodeCh(t *testing.T, events <-chan natsapi.HelmEvent, code int, timeout time.Duration) natsapi.HelmEvent {
	t.Helper()
	e, ok := waitCodeCh(events, code, timeout)
	if !ok {
		t.Fatalf("timeout waiting for activity code %d", code)
	}
	return e
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

func exitSignalFor(symbol string) strategy.Signal {
	return strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirExit,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	}
}

const simWait = 200 * time.Millisecond
