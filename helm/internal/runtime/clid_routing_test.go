package runtime_test

// Regression test for the client-order-id (clid) routing fix.
//
// The bug it guards: a WS full-fill can arrive before the REST PlaceOrder response
// returns the exchange order id. Before clid routing, the runtime had no way to map
// that early fill to the owning hand, so it took the deferred-buffer / "manual" path.
//
// With clid routing, the hand tracks the clid BEFORE calling PlaceOrder, so a fill
// that races ahead of the REST response still routes to the right hand. This test
// reproduces the exact race: the mock exchange delivers the WS fill from INSIDE
// PlaceOrder (i.e. before it returns) and returns status "new" (not "filled"), so the
// only way the position can open is via the WS path resolving the clid.
//
// go test -run TestClidRouting ./internal/runtime/ -count=1

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/clid"
)

// raceExchange implements exchange.Exchange + exchange.AccountStreamer. On PlaceOrder
// it fires a WS full-fill (carrying the request's ClientOrderID) BEFORE returning, then
// returns status "new" so the REST-immediate path does not apply the fill.
type raceExchange struct {
	mu        sync.Mutex
	onFill    func(exchange.WsFillEvent)
	fillPrice decimal.Decimal
	lastClid  chan string // receives the clid seen on PlaceOrder
}

func newRaceExchange(fillPrice float64) *raceExchange {
	return &raceExchange{
		fillPrice: decimal.NewFromFloat(fillPrice),
		lastClid:  make(chan string, 1),
	}
}

func (s *raceExchange) Name() string { return "race" }

func (s *raceExchange) PlaceOrder(_ context.Context, _ exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	const exID = "race-ex-1"
	s.mu.Lock()
	onFill := s.onFill
	s.mu.Unlock()

	// Surface the clid the runtime generated + tracked before this call.
	select {
	case s.lastClid <- req.ClientOrderID:
	default:
	}

	// Simulate the WS full-fill arriving BEFORE this REST response returns.
	if onFill != nil {
		onFill(exchange.WsFillEvent{
			OrderID:       exID,
			ClientOrderID: req.ClientOrderID, // echoed by the exchange
			TradeID:       "race-trade-1",
			Symbol:        req.Symbol,
			Side:          req.Side,
			Partial:       false,
			FilledQty:     req.Qty,
			FilledAvg:     s.fillPrice,
			Timestamp:     time.Now().UTC(),
		})
	}

	// Return status "new" — NOT filled — so the only path that can apply the fill is
	// the WS event above, which must resolve the clid to route correctly.
	return &exchange.OrderResult{
		ID:     exID,
		Symbol: req.Symbol,
		Side:   req.Side,
		Status: "new",
		Qty:    req.Qty,
	}, nil
}

func (s *raceExchange) StreamOrders(
	_ context.Context,
	_ exchange.Credentials,
	_ func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	_ func(exchange.BalanceEvent),
) error {
	s.mu.Lock()
	s.onFill = onFill
	s.mu.Unlock()
	return nil
}

func (s *raceExchange) GetOrder(_ context.Context, _ exchange.Credentials, id string) (*exchange.OrderResult, error) {
	// Report still-open so the poll path doesn't double-apply; the WS fill is authoritative.
	return &exchange.OrderResult{ID: id, Status: "new"}, nil
}

func (s *raceExchange) CancelOrder(_ context.Context, _ exchange.Credentials, _ string) error {
	return nil
}

func (s *raceExchange) ListOpenOrders(_ context.Context, _ exchange.Credentials, _ string) ([]exchange.OrderResult, error) {
	return nil, nil
}

func (s *raceExchange) ListPositions(_ context.Context, _ exchange.Credentials) ([]exchange.PositionResult, error) {
	return nil, nil
}

// TestClidRouting_WsFillBeforeRestResponse asserts that a WS full-fill arriving before
// the PlaceOrder REST response routes to the owning hand via the clid — the position
// opens and CodeOrderFilled fires on the hand's own event bus (orphan fills do not).
func TestClidRouting_WsFillBeforeRestResponse(t *testing.T) {
	const symbol = "BTCUSDT"
	ex := newRaceExchange(50_000)
	rt := buildSimRuntime(ex, 100_000, 10)
	defer rt.Stop()

	// Start the WS fill processor + register the onFill callback with the exchange.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.StartFillStreaming(ctx)

	h := addSimHand(rt, symbol, 0.001, 0, 0.3, 1)
	rt.UpdatePrice(symbol, decimal.NewFromFloat(50_000))
	h.Start()
	defer h.Stop()

	filledCh := h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))

	// The fill arrived via WS before PlaceOrder returned; it must route to this hand.
	mustWaitCodeCh(t, filledCh, runtime.CodeOrderFilled, simWait)

	// clid sent to the exchange must be a mallow-generated id.
	select {
	case clidVal := <-ex.lastClid:
		if !clid.IsOurClid(clidVal) {
			t.Fatalf("expected mallow clid prefix on PlaceOrder, got %q", clidVal)
		}
	case <-time.After(simWait):
		t.Fatal("PlaceOrder was never called")
	}

	// Position must be open — proves the WS fill was applied to the hand, not orphaned.
	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || pos.Qty.IsZero() {
		t.Fatal("expected open position after WS-before-REST fill; fill was not routed to the hand")
	}

	// The fill must have been counted as a clid route (not alias, not orphan).
	clidN, aliasN, orphanN := rt.FillRouteCounts()
	if clidN != 1 || aliasN != 0 || orphanN != 0 {
		t.Fatalf("expected fill route counts clid=1 alias=0 orphan=0, got clid=%d alias=%d orphan=%d",
			clidN, aliasN, orphanN)
	}
}
