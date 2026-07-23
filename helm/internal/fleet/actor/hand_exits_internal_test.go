package actor

// checkExits internal test — verifies the local exit monitor tags the
// delivered Signal with the correct strategy.ExitKind (sl/tp) instead of
// leaving it untagged. checkExits is unexported and only reachable via the
// run loop's ticker in production, so this lives in package actor (not
// actor_test) to call it directly; poslog event helpers mirror
// reconcile_test.go's recPlaced/recFilled (kept self-contained per that
// file's own precedent for duplicating across test files in this package).

import (
	"context"
	"encoding/json"
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
	"mallow/helm/internal/module/hand/domain"
)

func buildCheckExitsHand(t *testing.T, symbol string) (*Hand, *HelmRuntime) {
	t.Helper()
	pf := portfolio.New(decimal.NewFromFloat(10_000))
	rm := risk.New(risk.DefaultConfig(), pf)
	rt := NewHelmRuntime(uuid.New(), uuid.New(), uuid.New(), "test", pf, rm, nil, exchange.Credentials{}, nil, time.Now())
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(49_000)) // below the SL level set below

	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.DefaultSizingConfig())
	h := NewHand(uuid.New(), rt.HelmID, rt, strat, tact, false, 1, 10*time.Second,
		nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	h.Symbol = symbol

	// Seed one active leg via poslog replay (same mechanism as a real restart) —
	// checkExits requires h.pos.ActiveCount() > 0 or it treats the exit level as
	// stale (external close) instead of firing.
	tradeID := "seed-1"
	placed := poslog.OrderPlacedPayload{OrderID: tradeID, Symbol: symbol, Side: "buy", Qty: "0.01", Price: "50000", OrderType: "market"}
	placedPayload, _ := json.Marshal(placed)
	if err := h.pos.Apply(poslog.Event{ID: tradeID, TradeID: tradeID, Kind: poslog.KindOrderPlaced, Payload: placedPayload, At: time.Now()}); err != nil {
		t.Fatalf("seed KindOrderPlaced: %v", err)
	}
	filled := poslog.OrderFilledPayload{OrderID: tradeID, FillPrice: "50000", FillQty: "0.01", Source: "ws"}
	filledPayload, _ := json.Marshal(filled)
	if err := h.pos.Apply(poslog.Event{ID: tradeID + "_filled", TradeID: tradeID, Kind: poslog.KindOrderFilled, Payload: filledPayload, At: time.Now()}); err != nil {
		t.Fatalf("seed KindOrderFilled: %v", err)
	}

	h.exitLevels[tradeID] = exitLevel{
		Symbol:   symbol,
		Side:     "buy",
		StopLoss: decimal.NewFromFloat(49_500), // current price (49_000) is below this
	}
	return h, rt
}

func TestCheckExits_TagsStopLoss(t *testing.T) {
	const symbol = "BTCUSDT"
	h, _ := buildCheckExitsHand(t, symbol)

	h.checkExits()

	select {
	case sig := <-h.Signals:
		if sig.Direction != strategy.DirExit {
			t.Errorf("expected DirExit, got %s", sig.Direction)
		}
		if sig.ExitKind != strategy.ExitKindStopLoss {
			t.Errorf("expected ExitKind=StopLoss, got %q", sig.ExitKind)
		}
	default:
		t.Fatal("expected checkExits to deliver a signal onto h.Signals")
	}
}

// TestCheckExits_MultiLegSameSymbol_AttributesCorrectLeg reproduces the bug fixed by
// keying exitLevels by TradeID instead of symbol: a non-pyramid hand with MaxUnits=2
// can hold two independent legs on the same symbol, each with its own SL/TP. Under the
// old symbol-keyed map, seeding a second leg's exitLevels entry would silently overwrite
// the first's — this test proves both entries now coexist and checkExits attributes the
// triggered signal to the exact leg that tripped (via Signal.PositionID), leaving the
// other leg's exit level untouched.
func TestCheckExits_MultiLegSameSymbol_AttributesCorrectLeg(t *testing.T) {
	const symbol = "BTCUSDT"
	pf := portfolio.New(decimal.NewFromFloat(10_000))
	rm := risk.New(risk.DefaultConfig(), pf)
	rt := NewHelmRuntime(uuid.New(), uuid.New(), uuid.New(), "test", pf, rm, nil, exchange.Credentials{}, nil, time.Now())
	rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(49_000)) // below leg-1's SL; above leg-2's SL

	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.DefaultSizingConfig())
	h := NewHand(uuid.New(), rt.HelmID, rt, strat, tact, false, 2, 10*time.Second,
		nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	h.Symbol = symbol

	seedLeg := func(tradeID string) {
		placed := poslog.OrderPlacedPayload{OrderID: tradeID, Symbol: symbol, Side: "buy", Qty: "0.01", Price: "50000", OrderType: "market"}
		placedPayload, _ := json.Marshal(placed)
		if err := h.pos.Apply(poslog.Event{ID: tradeID, TradeID: tradeID, Kind: poslog.KindOrderPlaced, Payload: placedPayload, At: time.Now()}); err != nil {
			t.Fatalf("seed KindOrderPlaced(%s): %v", tradeID, err)
		}
		filled := poslog.OrderFilledPayload{OrderID: tradeID, FillPrice: "50000", FillQty: "0.01", Source: "ws"}
		filledPayload, _ := json.Marshal(filled)
		if err := h.pos.Apply(poslog.Event{ID: tradeID + "_filled", TradeID: tradeID, Kind: poslog.KindOrderFilled, Payload: filledPayload, At: time.Now()}); err != nil {
			t.Fatalf("seed KindOrderFilled(%s): %v", tradeID, err)
		}
	}
	seedLeg("leg-1")
	seedLeg("leg-2")

	// leg-1's SL is above the current price (49_000) — triggers.
	// leg-2's SL is below the current price — must NOT trigger, and must survive
	// leg-1's exit untouched (this is the exact collision the old symbol key had).
	h.exitLevels["leg-1"] = exitLevel{Symbol: symbol, Side: "buy", StopLoss: decimal.NewFromFloat(49_500)}
	h.exitLevels["leg-2"] = exitLevel{Symbol: symbol, Side: "buy", StopLoss: decimal.NewFromFloat(48_500)}

	h.checkExits()

	select {
	case sig := <-h.Signals:
		if sig.PositionID != "leg-1" {
			t.Errorf("expected triggered signal attributed to leg-1, got PositionID=%q", sig.PositionID)
		}
		if sig.ExitKind != strategy.ExitKindStopLoss {
			t.Errorf("expected ExitKind=StopLoss, got %q", sig.ExitKind)
		}
	default:
		t.Fatal("expected checkExits to deliver a signal for leg-1")
	}

	h.mu.RLock()
	leg2, ok := h.exitLevels["leg-2"]
	h.mu.RUnlock()
	if !ok {
		t.Fatal("leg-2's exitLevels entry was removed — collision with leg-1's trigger")
	}
	if !leg2.StopLoss.Equal(decimal.NewFromFloat(48_500)) {
		t.Errorf("leg-2's exitLevels entry was corrupted: stop_loss = %s, want 48500", leg2.StopLoss)
	}
	if _, stillThere := h.exitLevels["leg-1"]; stillThere {
		t.Error("leg-1's exitLevels entry should have been deleted after triggering")
	}
}

// cancelCallExchange implements exchange.Exchange plus (optionally) an atomic
// exchange.ExitOrderGroupCanceller, recording every CancelOrder / CancelExitOrderGroup
// call it receives — used to verify cancelExitOrders prefers the group-cancel path
// when a GroupID is known, and falls back to per-ID cancel when it isn't.
type cancelCallExchange struct {
	mu             sync.Mutex
	cancelOrderIDs []string
	groupCalls     []string      // groupID per call
	done           chan struct{} // closed after the goroutine's cancel work is applied
}

func (c *cancelCallExchange) Name() string { return "cancel-test" }
func (c *cancelCallExchange) PlaceOrder(context.Context, exchange.Credentials, exchange.OrderRequest) (*exchange.OrderResult, error) {
	return nil, nil
}
func (c *cancelCallExchange) GetOrder(context.Context, exchange.Credentials, string) (*exchange.OrderResult, error) {
	return nil, nil
}
func (c *cancelCallExchange) ListOpenOrders(context.Context, exchange.Credentials, string) ([]exchange.OrderResult, error) {
	return nil, nil
}
func (c *cancelCallExchange) ListPositions(context.Context, exchange.Credentials) ([]exchange.PositionResult, error) {
	return nil, nil
}
func (c *cancelCallExchange) CancelOrder(_ context.Context, _ exchange.Credentials, orderID string) error {
	c.mu.Lock()
	c.cancelOrderIDs = append(c.cancelOrderIDs, orderID)
	n := len(c.cancelOrderIDs)
	c.mu.Unlock()
	if n == 1 {
		close(c.done)
	}
	return nil
}
func (c *cancelCallExchange) CancelExitOrderGroup(_ context.Context, _ exchange.Credentials, _ string, _ exchange.MarketKind, groupID string) error {
	c.mu.Lock()
	c.groupCalls = append(c.groupCalls, groupID)
	c.mu.Unlock()
	close(c.done)
	return nil
}

// plainCancelExchange implements only the base exchange.Exchange interface — no
// CancelExitOrderGroup at all — modeling Bybit/fbinance/Alpaca, which have no true
// exchange-side bracket group. cancelExitOrders must fall back to per-ID cancel here
// regardless of whether a GroupID happens to be set.
type plainCancelExchange struct {
	mu             sync.Mutex
	cancelOrderIDs []string
	done           chan struct{}
}

func (c *plainCancelExchange) Name() string { return "plain-cancel-test" }
func (c *plainCancelExchange) PlaceOrder(context.Context, exchange.Credentials, exchange.OrderRequest) (*exchange.OrderResult, error) {
	return nil, nil
}
func (c *plainCancelExchange) GetOrder(context.Context, exchange.Credentials, string) (*exchange.OrderResult, error) {
	return nil, nil
}
func (c *plainCancelExchange) ListOpenOrders(context.Context, exchange.Credentials, string) ([]exchange.OrderResult, error) {
	return nil, nil
}
func (c *plainCancelExchange) ListPositions(context.Context, exchange.Credentials) ([]exchange.PositionResult, error) {
	return nil, nil
}
func (c *plainCancelExchange) CancelOrder(_ context.Context, _ exchange.Credentials, orderID string) error {
	c.mu.Lock()
	c.cancelOrderIDs = append(c.cancelOrderIDs, orderID)
	n := len(c.cancelOrderIDs)
	c.mu.Unlock()
	if n == 1 {
		close(c.done)
	}
	return nil
}

func buildCancelTestHand(t *testing.T, ex exchange.Exchange, tradeID, symbol, groupID string) *Hand {
	t.Helper()
	pf := portfolio.New(decimal.NewFromFloat(10_000))
	rm := risk.New(risk.DefaultConfig(), pf)
	rt := NewHelmRuntime(uuid.New(), uuid.New(), uuid.New(), "test", pf, rm, ex, exchange.Credentials{}, nil, time.Now())
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.DefaultSizingConfig())
	h := NewHand(uuid.New(), rt.HelmID, rt, strat, tact, false, 1, 10*time.Second,
		nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	h.Symbol = symbol
	h.exitLevels[tradeID] = exitLevel{
		Symbol:           symbol,
		Side:             "buy",
		ExchangeOrderIDs: []string{"order-1", "order-2"},
		GroupID:          groupID,
	}
	return h
}

func TestCancelExitOrders_PrefersGroupCancel(t *testing.T) {
	fake := &cancelCallExchange{done: make(chan struct{})}
	h := buildCancelTestHand(t, fake, "trade-1", "BTCUSDT", "group-1")

	h.mu.Lock()
	h.cancelExitOrders(context.Background(), "trade-1", "BTCUSDT", "")
	h.mu.Unlock()

	select {
	case <-fake.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelExitOrders did not call the exchange within timeout")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.groupCalls) != 1 || fake.groupCalls[0] != "group-1" {
		t.Errorf("expected exactly one CancelExitOrderGroup(group-1) call, got %v", fake.groupCalls)
	}
	if len(fake.cancelOrderIDs) != 0 {
		t.Errorf("expected no per-ID CancelOrder calls when a group id is known, got %v", fake.cancelOrderIDs)
	}
}

func TestCancelExitOrders_FallsBackToPerID_WhenNoGroupID(t *testing.T) {
	fake := &cancelCallExchange{done: make(chan struct{})}
	h := buildCancelTestHand(t, fake, "trade-1", "BTCUSDT", "") // no GroupID

	h.mu.Lock()
	h.cancelExitOrders(context.Background(), "trade-1", "BTCUSDT", "")
	h.mu.Unlock()

	select {
	case <-fake.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelExitOrders did not call the exchange within timeout")
	}
	// Give the second per-ID call (order-2) a moment to land too — done only
	// signals the first.
	time.Sleep(50 * time.Millisecond)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.groupCalls) != 0 {
		t.Errorf("expected no group-cancel calls without a GroupID, got %v", fake.groupCalls)
	}
	if len(fake.cancelOrderIDs) != 2 {
		t.Errorf("expected per-ID CancelOrder for both order ids, got %v", fake.cancelOrderIDs)
	}
}

// TestCancelExitOrders_FallsBackToPerID_WhenExchangeHasNoGroupCanceller models
// Bybit/fbinance/Alpaca: no true exchange-side bracket group, so the adapter never
// implements exchange.ExitOrderGroupCanceller at all. Even with a (meaningless) GroupID
// set on the leg, cancelExitOrders must not attempt a group call — the type assertion
// itself fails, exercising the `hasGroupCanceller` guard independently of `groupID != ""`.
func TestCancelExitOrders_FallsBackToPerID_WhenExchangeHasNoGroupCanceller(t *testing.T) {
	fake := &plainCancelExchange{done: make(chan struct{})}
	h := buildCancelTestHand(t, fake, "trade-1", "BTCUSDT", "group-1")

	h.mu.Lock()
	h.cancelExitOrders(context.Background(), "trade-1", "BTCUSDT", "")
	h.mu.Unlock()

	select {
	case <-fake.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelExitOrders did not call the exchange within timeout")
	}
	time.Sleep(50 * time.Millisecond)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.cancelOrderIDs) != 2 {
		t.Errorf("expected per-ID CancelOrder for both order ids, got %v", fake.cancelOrderIDs)
	}
}
