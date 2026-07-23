package actor

// HelmRuntime trade-actor tests: verifies the actor conversion (helm_actor.go)
// correctly serializes leverage across hands and computes proportional fee
// attribution, without touching the mutex it replaced. package actor (not
// actor_test) so seeding a hand's position can use h.pos.Apply directly,
// mirroring hand_exits_internal_test.go's precedent for this kind of setup.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/risk"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/core/tactics"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/module/hand/domain"
)

// leverageCallExchange implements exchange.Exchange + exchange.LeverageSetter,
// recording every SetLeverage call it receives.
type leverageCallExchange struct {
	mu    sync.Mutex
	calls []int // leverage value per call
}

func (e *leverageCallExchange) Name() string { return "leverage-test" }
func (e *leverageCallExchange) PlaceOrder(context.Context, exchange.Credentials, exchange.OrderRequest) (*exchange.OrderResult, error) {
	return nil, nil
}
func (e *leverageCallExchange) GetOrder(context.Context, exchange.Credentials, string) (*exchange.OrderResult, error) {
	return nil, nil
}
func (e *leverageCallExchange) CancelOrder(context.Context, exchange.Credentials, string) error {
	return nil
}
func (e *leverageCallExchange) ListOpenOrders(context.Context, exchange.Credentials, string) ([]exchange.OrderResult, error) {
	return nil, nil
}
func (e *leverageCallExchange) ListPositions(context.Context, exchange.Credentials) ([]exchange.PositionResult, error) {
	return nil, nil
}
func (e *leverageCallExchange) SetLeverage(_ context.Context, _ exchange.Credentials, _ string, leverage int, _ string) error {
	e.mu.Lock()
	e.calls = append(e.calls, leverage)
	e.mu.Unlock()
	return nil
}

// TestEnsureLeverage_CrossHandRace reproduces the bug fixed by moving leverage
// ownership to HelmRuntime's actor: two hands on the same helm requesting
// leverage for the same symbol (previously tracked per-Hand, so both would call
// SetLeverage independently) must now result in exactly one SetLeverage call —
// the actor's leverageSet map is the single source of truth across all hands.
func TestEnsureLeverage_CrossHandRace(t *testing.T) {
	ex := &leverageCallExchange{}
	pf := portfolio.New(decimal.NewFromFloat(10_000))
	rm := risk.New(risk.DefaultConfig(), pf)
	rt := NewHelmRuntime(uuid.New(), uuid.New(), uuid.New(), "test", pf, rm, ex, exchange.Credentials{}, nil, time.Now())

	ctx := context.Background()
	// Hand A and Hand B both configured for 10x isolated on the same symbol —
	// simulating two hands independently deciding to enter BTCUSDT futures.
	futuresA := &domain.FuturesConfig{Leverage: 10, MarginType: "isolated"}
	futuresB := &domain.FuturesConfig{Leverage: 10, MarginType: "isolated"}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); rt.EnsureLeverage(ctx, "BTCUSDT", futuresA) }()
	go func() { defer wg.Done(); rt.EnsureLeverage(ctx, "BTCUSDT", futuresB) }()
	wg.Wait()

	ex.mu.Lock()
	defer ex.mu.Unlock()
	if len(ex.calls) != 1 {
		t.Errorf("expected exactly one SetLeverage call across both hands, got %d: %v", len(ex.calls), ex.calls)
	}
}

// feeAttributionHand builds a hand with a single active leg of the given qty in
// symbol (poslog-seeded, same mechanism as hand_exits_internal_test.go's
// buildCheckExitsHand), subscribed to its own event bus so the test can observe
// CodeFeeAttributed. qty.IsZero() leaves the hand flat (no leg at all).
func feeAttributionHand(t *testing.T, rt *HelmRuntime, symbol string, qty decimal.Decimal) *Hand {
	t.Helper()
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.DefaultSizingConfig())
	h := NewHand(uuid.New(), rt.HelmID, rt, strat, tact, false, 1, 0,
		nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	h.Symbol = symbol
	h.EnableEventSink()
	rt.AddHand(h, &domain.Hand{ID: h.ID(), HelmID: rt.HelmID, Symbols: domain.StringSlice{symbol}})

	if qty.IsPositive() {
		tradeID := "seed-" + h.id.String()
		placed, _ := json.Marshal(poslog.OrderPlacedPayload{
			OrderID: tradeID, Symbol: symbol, Side: "buy", Qty: qty.String(), Price: "50000", OrderType: "market",
		})
		if err := h.pos.Apply(poslog.Event{ID: tradeID, TradeID: tradeID, Kind: poslog.KindOrderPlaced, Payload: placed, At: time.Now()}); err != nil {
			t.Fatalf("seed KindOrderPlaced: %v", err)
		}
		filled, _ := json.Marshal(poslog.OrderFilledPayload{OrderID: tradeID, FillPrice: "50000", FillQty: qty.String(), Source: "ws"})
		if err := h.pos.Apply(poslog.Event{ID: tradeID + "_filled", TradeID: tradeID, Kind: poslog.KindOrderFilled, Payload: filled, At: time.Now()}); err != nil {
			t.Fatalf("seed KindOrderFilled: %v", err)
		}
	}
	return h
}

// TestReportFeeEvent_ProportionalAttribution seeds two hands with different
// position sizes in the same symbol plus a third hand with no position at all,
// then asserts each of the first two receives a CodeFeeAttributed event
// proportional to its qty share, and the flat hand receives nothing.
func TestReportFeeEvent_ProportionalAttribution(t *testing.T) {
	const symbol = "BTCUSDT"
	pf := portfolio.New(decimal.NewFromFloat(10_000))
	rm := risk.New(risk.DefaultConfig(), pf)
	rt := NewHelmRuntime(uuid.New(), uuid.New(), uuid.New(), "test", pf, rm, nil, exchange.Credentials{}, nil, time.Now())

	hA := feeAttributionHand(t, rt, symbol, decimal.NewFromFloat(3)) // 3/4 share
	hB := feeAttributionHand(t, rt, symbol, decimal.NewFromFloat(1)) // 1/4 share
	hFlat := feeAttributionHand(t, rt, symbol, decimal.Zero)         // no position — must get nothing

	evA := hA.Subscribe(8)
	evB := hB.Subscribe(8)
	evFlat := hFlat.Subscribe(8)

	rt.ReportFeeEvent(FeeEvent{Kind: "funding", Symbol: symbol, Amount: decimal.NewFromFloat(-8)})

	gotA := waitFeeAttributed(t, evA)
	gotB := waitFeeAttributed(t, evB)

	wantA := decimal.NewFromFloat(-6) // -8 * 3/4
	wantB := decimal.NewFromFloat(-2) // -8 * 1/4
	if !decimalCloseEnough(gotA, wantA) {
		t.Errorf("hand A share: got %s, want %s", gotA, wantA)
	}
	if !decimalCloseEnough(gotB, wantB) {
		t.Errorf("hand B share: got %s, want %s", gotB, wantB)
	}

	select {
	case ev := <-evFlat:
		t.Errorf("flat hand should not receive a fee event, got %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

func waitFeeAttributed(t *testing.T, ch <-chan natsapi.HelmEvent) decimal.Decimal {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.Code != CodeFeeAttributed {
			t.Fatalf("expected CodeFeeAttributed, got code %d", ev.Code)
		}
		// Reason is "kind=<kind> amount=<amount>" (see applyFeeEvent) — split on the
		// amount= marker rather than fmt.Sscanf, which can't Scan into decimal.Decimal.
		const marker = "amount="
		idx := strings.Index(ev.Reason, marker)
		if idx < 0 {
			t.Fatalf("fee reason %q missing %q", ev.Reason, marker)
		}
		amount, err := decimal.NewFromString(ev.Reason[idx+len(marker):])
		if err != nil {
			t.Fatalf("parse fee amount from reason %q: %v", ev.Reason, err)
		}
		return amount
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CodeFeeAttributed event")
		return decimal.Zero
	}
}

func decimalCloseEnough(a, b decimal.Decimal) bool {
	return a.Sub(b).Abs().LessThan(decimal.NewFromFloat(0.0001))
}
