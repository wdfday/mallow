// E2E tests: signal → order placement pipeline.
//
// Tests that Hand.run() correctly processes a DeliverSignal call and submits
// an order to the real exchange sandbox/demo API.
//
// Each sub-test skips when its credentials env-vars are not set.
//
// Environment variables:
//
//	BINANCE_API_KEY / BINANCE_API_SECRET     (demo-api.binance.com)
//	OKX_API_KEY / OKX_API_SECRET / OKX_PASSPHRASE (simulated trading)
//	BYBIT_API_KEY / BYBIT_API_SECRET         (api-demo.bybit.com)
//	ALPACA_API_KEY / ALPACA_API_SECRET       (paper-api.alpaca.markets)
//
// go test -v -run TestSignalToOrder ./internal/runtime/ -timeout 60s
package integration_test

import (
	"context"
	"os"
	"strings"
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
	"mallow/helm/internal/fleet/actor/eventcode"
	signalfollower "mallow/helm/internal/fleet/actor/signal-follower"
	"mallow/helm/internal/infra/exchange"
	alpacaact "mallow/helm/internal/infra/exchange/alpaca/act"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	bybitact "mallow/helm/internal/infra/exchange/bybit/act"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
	"mallow/helm/internal/infra/natsapi"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// fillNotify subscribes to the hand event bus and sends the first CodeOrderFilled event.
// Subscribe is created once before the goroutine loops — safe even with synchronous fills.
func fillNotify(hand *signalfollower.Hand, timeout time.Duration) <-chan natsapi.HelmEvent {
	events := hand.Subscribe(64) // create once, outside the loop
	ch := make(chan natsapi.HelmEvent, 1)
	go func() {
		deadline := time.After(timeout)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					close(ch)
					return
				}
				if ev.Code == eventcode.CodeOrderFilled {
					ch <- ev
					return
				}
			case <-deadline:
				close(ch)
				return
			}
		}
	}()
	return ch
}

// codeNotify subscribes to the hand event bus and sends the first event matching code.
// Generic building block for one-off listeners (e.g. CodePositionExtClosed) that don't
// warrant their own dedicated fillNotify/orderNotify-style wrapper.
func codeNotify(hand *signalfollower.Hand, code int, timeout time.Duration) <-chan natsapi.HelmEvent {
	events := hand.Subscribe(64) // create once, outside the loop
	ch := make(chan natsapi.HelmEvent, 1)
	go func() {
		deadline := time.After(timeout)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					close(ch)
					return
				}
				if ev.Code == code {
					ch <- ev
					return
				}
			case <-deadline:
				close(ch)
				return
			}
		}
	}()
	return ch
}

// orderNotify subscribes to the hand event bus and sends the first CodeOrderPlaced or CodeOrderFailed event.
func orderNotify(hand *signalfollower.Hand, timeout time.Duration) <-chan natsapi.HelmEvent {
	events := hand.Subscribe(64) // create once, outside the loop
	ch := make(chan natsapi.HelmEvent, 1)
	go func() {
		deadline := time.After(timeout)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					close(ch)
					return
				}
				if ev.Code == eventcode.CodeOrderPlaced || ev.Code == eventcode.CodeOrderFailed {
					ch <- ev
					return
				}
			case <-deadline:
				close(ch)
				return
			}
		}
	}()
	return ch
}

// newTestRuntime builds a minimal HelmRuntime with a seeded portfolio.
// capital is the starting cash balance (e.g. 100000 USDT).
func newTestRuntime(ex exchange.Exchange, creds exchange.Credentials, capital decimal.Decimal) *actor.HelmRuntime {
	pf := portfolio.New(capital)
	rm := risk.New(risk.DefaultConfig(), pf)
	return actor.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		ex.Name(), pf, rm, ex, creds, nil, time.Now(),
	)
}

// newFixedQtyHand creates a Hand with FixedQty sizing (no poslog — dev mode).
func newFixedQtyHand(rt *actor.HelmRuntime, qty decimal.Decimal) *signalfollower.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: qty,
	})
	h := signalfollower.NewHand(uuid.New(), rt.HelmID, rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	h.EnableEventSink()
	return h
}

// longSig builds a long entry signal with strength 1.0.
func longSig(symbol string) strategy.Signal {
	return strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirLong,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	}
}

// cancelAllOrders cancels any pending orders found in the hand after a test.
func cancelAllOrders(t *testing.T, rt *actor.HelmRuntime, hand *signalfollower.Hand) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, o := range hand.Orders() {
		switch o.Status {
		case "new", "accepted", "pending_new", "partially_filled", "submitted", "open":
			if err := rt.Exchange.CancelOrder(ctx, rt.Creds, o.ID); err != nil {
				t.Logf("cleanup CancelOrder %s: %v (non-fatal)", o.ID, err)
			} else {
				t.Logf("cleanup: cancelled order %s", o.ID)
			}
		}
	}
}

// isBalanceError reports whether an order-failed reason is a sandbox balance issue.
// These are skipped (not failed) since they require a manual sandbox top-up.
func isBalanceError(reason string) bool {
	lower := strings.ToLower(reason)
	return strings.Contains(lower, "insufficient") ||
		strings.Contains(lower, "balance") ||
		strings.Contains(lower, "margin")
}

// exitSig builds an exit signal with strength 1.0 (urgent — always passes the filter).
func exitSig(symbol string) strategy.Signal {
	return strategy.Signal{
		Symbol:     symbol,
		Direction:  strategy.DirExit,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
	}
}

// orderNotifyNew subscribes to the hand event bus and sends the first
// CodeOrderPlaced or CodeOrderFailed event whose OrderID differs from excludeID.
func orderNotifyNew(hand *signalfollower.Hand, excludeID string, timeout time.Duration) <-chan natsapi.HelmEvent {
	events := hand.Subscribe(64)
	ch := make(chan natsapi.HelmEvent, 1)
	go func() {
		deadline := time.After(timeout)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					close(ch)
					return
				}
				if (ev.Code == eventcode.CodeOrderPlaced || ev.Code == eventcode.CodeOrderFailed) &&
					ev.OrderID != excludeID {
					ch <- ev
					return
				}
			case <-deadline:
				close(ch)
				return
			}
		}
	}()
	return ch
}

// fillNotifyOrder subscribes to the hand event bus and sends the first
// CodeOrderFilled event whose OrderID matches targetID.
func fillNotifyOrder(hand *signalfollower.Hand, targetID string, timeout time.Duration) <-chan natsapi.HelmEvent {
	events := hand.Subscribe(64)
	ch := make(chan natsapi.HelmEvent, 1)
	go func() {
		deadline := time.After(timeout)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					close(ch)
					return
				}
				if ev.Code == eventcode.CodeOrderFilled && ev.OrderID == targetID {
					ch <- ev
					return
				}
			case <-deadline:
				close(ch)
				return
			}
		}
	}()
	return ch
}

// recvEvent waits for either a delivered HelmEvent on ch or a timeout, returning
// ok=false in both the "channel closed" and "wall-clock timeout" cases.
//
// fillNotify/orderNotify/orderNotifyNew/fillNotifyOrder each run their own internal
// goroutine that closes ch once ITS OWN deadline elapses. A bare `select { case e :=
// <-ch: ...; case <-time.After(d): ... }` at the call site races that internal close
// against its own timer: if the internal deadline wins, the receive on the
// already-closed channel returns a zero-value HelmEvent immediately, and code that
// doesn't check the second (ok) return value treats that zero value as a real event
// — e.g. logging "filled: order_id= qty=0 fill_price=0" or, worse, proceeding past a
// nil-position check as if a fill had actually happened. Route every receive through
// this helper instead of a bare select so ok=false is impossible to miss.
func recvEvent(ch <-chan natsapi.HelmEvent, timeout time.Duration) (natsapi.HelmEvent, bool) {
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(timeout):
		return natsapi.HelmEvent{}, false
	}
}

// portfolioQty reads rt.Portfolio.GetPosition(symbol).Qty, or zero if flat.
//
// rt.Portfolio is helm-wide, not per-hand: every fill (hand-owned, orphan, or
// gap-recovery) funnels through HelmRuntime.ReportFill, which calls MarkSyncDirty
// and schedules a debounced (3s) full-account resync (helm_trading.go). That resync
// pulls the REAL exchange balance for the symbol — correctly, by design — which on a
// shared demo/paper account can include ETH/BTC this test never touched (pre-funded
// balance, other tests' dust, etc). A reading taken here is never guaranteed to be
// "just this hand's contribution."
//
// Because of this, NEVER assert an absolute value against portfolioQty (e.g. "== 0"
// after a hand's own exit) — it will flake depending on whether the debounced resync
// has landed yet and what unrelated balance the account happens to hold. Instead,
// capture a baseline before the action under test, capture again after (allowing the
// same ~3s+ for the resync to settle each time), and assert on the DELTA between the
// two readings — that isolates exactly what this hand's own trade did, independent of
// whatever the shared account balance happened to be at either point in time.
func portfolioQty(rt *actor.HelmRuntime, symbol string) decimal.Decimal {
	if pos := rt.Portfolio.GetPosition(symbol); pos != nil {
		return pos.Qty
	}
	return decimal.Zero
}

// ── Binance Demo ──────────────────────────────────────────────────────────────

func TestSignalToOrder_Binance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set in creds_test.go")
	}

	const symbol = "BTCUSDT"
	ex := binanceact.New(true) // testnet → demo-api.binance.com
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	// Fetch live price so ProcessTrade can size the fixed-qty order.
	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.MarketData.SetPrice(symbol, price)
		t.Logf("current %s price: %s", symbol, price)
	} else {
		t.Logf("price fetch failed (%v) — ProcessTrade will fetch inline", err)
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer hand.Stop()

	notify := orderNotify(hand, 15*time.Second)

	hand.DeliverSignal(longSig(symbol))

	select {
	case entry := <-notify:
		t.Logf("result: code=%d symbol=%s side=%s qty=%s price=%s order_id=%s reason=%s",
			entry.Code, entry.Symbol, entry.Side, entry.Qty, entry.Price, entry.OrderID, entry.Reason)
		if entry.Code == eventcode.CodeOrderFailed {
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
			t.Errorf("order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout: no order placed or failed within 15s")
	}

	orders := hand.Orders()
	if len(orders) == 0 {
		t.Fatal("hand.Orders() is empty after signal")
	}
	t.Logf("orders[0]: id=%s status=%s filled_qty=%s filled_avg=%s",
		orders[0].ID, orders[0].Status, orders[0].FilledQty, orders[0].FilledAvg)

	cancelAllOrders(t, rt, hand)
}

// ── OKX Simulated ─────────────────────────────────────────────────────────────

func TestSignalToOrder_OKX(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if okxPaperAPIKey == "" {
		t.Skip("OKX paper credentials not set in creds_test.go")
	}

	const symbol = "BTC-USDT"
	ex := okxact.New(okxact.Config{Paper: true})
	creds := exchange.Credentials{APIKey: okxPaperAPIKey, APISecret: okxPaperAPISecret, Passphrase: okxPaperPassphrase}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.MarketData.SetPrice(symbol, price)
		t.Logf("current %s price: %s", symbol, price)
	} else {
		t.Logf("price fetch failed (%v) — ProcessTrade will fetch inline", err)
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer hand.Stop()

	notify := orderNotify(hand, 15*time.Second)

	hand.DeliverSignal(longSig(symbol))

	select {
	case entry := <-notify:
		t.Logf("result: code=%d symbol=%s side=%s qty=%s price=%s order_id=%s reason=%s",
			entry.Code, entry.Symbol, entry.Side, entry.Qty, entry.Price, entry.OrderID, entry.Reason)
		if entry.Code == eventcode.CodeOrderFailed {
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
			t.Errorf("order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout: no order placed or failed within 15s")
	}

	orders := hand.Orders()
	if len(orders) == 0 {
		t.Fatal("hand.Orders() is empty after signal")
	}
	t.Logf("orders[0]: id=%s status=%s filled_qty=%s filled_avg=%s",
		orders[0].ID, orders[0].Status, orders[0].FilledQty, orders[0].FilledAvg)

	cancelAllOrders(t, rt, hand)
}

// ── Bybit Demo ────────────────────────────────────────────────────────────────

func TestSignalToOrder_Bybit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if bybitTestAPIKey == "" {
		t.Skip("Bybit demo credentials not set in creds_test.go")
	}

	const symbol = "BTCUSDT"
	ex := bybitact.New(bybitact.Config{Paper: true})
	creds := exchange.Credentials{APIKey: bybitTestAPIKey, APISecret: bybitTestAPISecret}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	// Bybit does not implement PriceFetcher — seed the price cache manually.
	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.MarkPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.MarketData.SetPrice(symbol, price)
		t.Logf("current %s mark price: %s", symbol, price)
	} else {
		t.Logf("mark price fetch failed (%v) — using approximate seed", err)
		rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(65_000))
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer hand.Stop()

	notify := orderNotify(hand, 15*time.Second)

	hand.DeliverSignal(longSig(symbol))

	select {
	case entry := <-notify:
		t.Logf("result: code=%d symbol=%s side=%s qty=%s price=%s order_id=%s reason=%s",
			entry.Code, entry.Symbol, entry.Side, entry.Qty, entry.Price, entry.OrderID, entry.Reason)
		if entry.Code == eventcode.CodeOrderFailed {
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
			t.Errorf("order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout: no order placed or failed within 15s")
	}

	orders := hand.Orders()
	if len(orders) == 0 {
		t.Fatal("hand.Orders() is empty after signal")
	}
	t.Logf("orders[0]: id=%s status=%s filled_qty=%s filled_avg=%s",
		orders[0].ID, orders[0].Status, orders[0].FilledQty, orders[0].FilledAvg)

	cancelAllOrders(t, rt, hand)
}

// ── Alpaca Paper ──────────────────────────────────────────────────────────────

func TestSignalToOrder_Alpaca(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if alpacaPaperAPIKey == "" {
		t.Skip("Alpaca paper credentials not set in creds_test.go")
	}

	const symbol = "AAPL"
	ex := alpacaact.New(alpacaact.Config{})
	creds := exchange.Credentials{APIKey: alpacaPaperAPIKey, APISecret: alpacaPaperAPISecret}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.MarketData.SetPrice(symbol, price)
		t.Logf("current %s price: %s", symbol, price)
	} else {
		t.Logf("price fetch failed (%v) — ProcessTrade will fetch inline", err)
	}

	// Alpaca requires markets open for US equities market orders.
	// Use 1 share — minimum whole-share quantity.
	hand := newFixedQtyHand(rt, decimal.NewFromFloat(1))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer hand.Stop()

	notify := orderNotify(hand, 15*time.Second)

	hand.DeliverSignal(longSig(symbol))

	select {
	case entry := <-notify:
		t.Logf("result: code=%d symbol=%s side=%s qty=%s price=%s order_id=%s reason=%s",
			entry.Code, entry.Symbol, entry.Side, entry.Qty, entry.Price, entry.OrderID, entry.Reason)
		// Market orders on Alpaca paper may fail outside trading hours; log but don't fail the test.
		if entry.Code == eventcode.CodeOrderFailed {
			t.Logf("order not placed: %s (market may be closed)", entry.Reason)
			return
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout: no order placed or failed within 15s")
	}

	orders := hand.Orders()
	if len(orders) == 0 {
		t.Fatal("hand.Orders() is empty after signal")
	}
	t.Logf("orders[0]: id=%s status=%s filled_qty=%s filled_avg=%s",
		orders[0].ID, orders[0].Status, orders[0].FilledQty, orders[0].FilledAvg)

	cancelAllOrders(t, rt, hand)
}

// ── Round-trip helpers ────────────────────────────────────────────────────────

// waitFills subscribes to the hand event bus and waits until n CodeOrderFilled
// events are received or timeout elapses.
// NOTE: subscribe BEFORE delivering the signal to avoid missing synchronous fills.
// Prefer waitFillsCh when subscribing before the signal delivery is required.
func waitFills(hand *signalfollower.Hand, n int, timeout time.Duration) bool {
	return waitFillsCh(hand.Subscribe(64), n, timeout)
}

// waitFillsCh waits on a pre-created events channel until n CodeOrderFilled events
// are received or timeout elapses. Use when you need to subscribe BEFORE delivering
// the signal (e.g. to catch REST-immediate fills that fire synchronously).
func waitFillsCh(events <-chan natsapi.HelmEvent, n int, timeout time.Duration) bool {
	count := 0
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return count >= n
			}
			if ev.Code == eventcode.CodeOrderFilled {
				count++
				if count >= n {
					return true
				}
			}
		case <-deadline:
			return false
		}
	}
}

// logHandState dumps hand health, metrics, and portfolio position.
func logHandState(t *testing.T, rt *actor.HelmRuntime, hand *signalfollower.Hand, symbol string) {
	t.Helper()
	h := hand.Health()
	m := hand.Metrics()
	t.Logf("── state dump ─────────────────────────────────────────────")
	t.Logf("health: status=%s uptime=%s last_error=%s",
		h.Status, h.Uptime, h.LastError)
	t.Logf("metrics: signals=%d filtered=%d dropped=%d trades_approved=%d orders_placed=%d orders_filled=%d orders_failed=%d pnl=%s",
		m.SignalsReceived, m.SignalsFiltered, m.SignalsDropped,
		m.TradesApproved, m.OrdersPlaced, m.OrdersFilled, m.OrdersFailed, m.TotalPnL)
	if pos := rt.Portfolio.GetPosition(symbol); pos != nil {
		t.Logf("portfolio position: symbol=%s qty=%s avg_px=%s", pos.Symbol, pos.Qty, pos.AvgPrice)
	} else {
		t.Logf("portfolio position: flat (nil)")
	}
	t.Logf("──────────────────────────────────────────────────────────")
}

// ── eventRecorder: full-run event dump ────────────────────────────────────────

// eventRecorder captures every event a hand emits, in arrival order, for a full
// post-mortem dump at the end of a test. hand.emitEvent always also calls
// h.helmRuntime.EmitEvent (helm.go) — so this is effectively "helm and hand"
// combined, since every test env in this package runs exactly one hand per helm.
type eventRecorder struct {
	mu     sync.Mutex
	events []recordedEvent
}

// recordedEvent pairs a HelmEvent with the recorder's own receipt time. e.At is NOT
// usable here: hand.emitEvent (hand_control.go) passes ev by value to both
// h.helmRuntime.EmitEvent (which stamps .At on ITS OWN copy) and h.eventBus.publish
// (which gets the original, never-stamped copy) — so every event arriving on the
// test event bus always has a zero-value .At. recvAt is this recorder's own
// timestamp instead, close enough for a test post-mortem.
type recordedEvent struct {
	ev     natsapi.HelmEvent
	recvAt time.Time
}

// recordEvents subscribes to hand's event bus immediately with a generous buffer —
// this is a whole-test capture, not a wait for one specific event — and starts
// draining it in the background. Call as early as possible (right after
// EnableEventSink/AddHand, before Start()) so nothing from the very first signal
// onward is missed. Typically paired with `defer rec.dump(t, "...")` right after,
// so the dump always runs — pass or fail.
func recordEvents(hand *signalfollower.Hand) *eventRecorder {
	r := &eventRecorder{}
	ch := hand.Subscribe(1024)
	go func() {
		for ev := range ch {
			r.mu.Lock()
			r.events = append(r.events, recordedEvent{ev: ev, recvAt: time.Now()})
			r.mu.Unlock()
		}
	}()
	return r
}

// dump prints every event captured so far, oldest first, framed in a box. Safe to
// call more than once (e.g. once from a t.Fatal path mid-test, once deferred at
// the end) — each call reflects whatever has arrived up to that point.
func (r *eventRecorder) dump(t *testing.T, title string) {
	t.Helper()
	r.mu.Lock()
	events := append([]recordedEvent(nil), r.events...)
	r.mu.Unlock()

	t.Logf("╔═ %s (%d events) ═══════════════════════════════════", title, len(events))
	for i, re := range events {
		e := re.ev
		name := eventcode.CodeNames[e.Code]
		if name == "" {
			name = "?"
		}
		t.Logf("║ %2d. [%s] %-28s code=%-5d hand=%s symbol=%-10s side=%-4s qty=%-14s price=%-10s order=%s",
			i+1, re.recvAt.Format("15:04:05.000"), name, e.Code, e.HandID, e.Symbol, e.Side, e.Qty, e.Price, e.OrderID)
		if e.Reason != "" {
			t.Logf("║      reason: %s", e.Reason)
		}
	}
	t.Logf("╚═══════════════════════════════════════════════════════════")
}

// ── Binance Demo — round-trip ─────────────────────────────────────────────────

func TestSignalRoundTrip_Binance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set in creds_test.go")
	}

	const symbol = "BTCUSDT"
	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.MarketData.SetPrice(symbol, price)
		t.Logf("current %s price: %s", symbol, price)
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer func() {
		time.Sleep(5 * time.Second)
		logHandState(t, rt, hand, symbol)
		hand.Stop()
	}()

	// Entry.
	fillEvts1 := hand.Subscribe(64)
	entryNotify := orderNotify(hand, 15*time.Second)
	hand.DeliverSignal(longSig(symbol))
	var entryOrderID string
	select {
	case entry := <-entryNotify:
		t.Logf("entry: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		entryOrderID = entry.OrderID
		if entry.Code == eventcode.CodeOrderFailed {
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
			t.Fatalf("entry order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for entry order")
	}

	if !waitFillsCh(fillEvts1, 1, 20*time.Second) {
		t.Fatal("timeout waiting for entry fill")
	}

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected open long position after entry fill")
	}
	t.Logf("position after entry: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Exit. Use the captured entryOrderID instead of hand.Orders()[0].ID —
	// pollOrders prunes filled orders from h.orders, so the slice may be empty.
	fillEvts2 := hand.Subscribe(64)
	exitNotify := orderNotifyNew(hand, entryOrderID, 15*time.Second)
	hand.DeliverSignal(exitSig(symbol))
	select {
	case entry := <-exitNotify:
		t.Logf("exit: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == eventcode.CodeOrderFailed {
			t.Logf("exit order failed (non-fatal for sandbox): %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for exit order")
	}

	if !waitFillsCh(fillEvts2, 2, 20*time.Second) {
		t.Log("exit fill not confirmed within 20s — order may still be pending")
	}

	cancelAllOrders(t, rt, hand)
}

// ── OKX Simulated — round-trip ────────────────────────────────────────────────

func TestSignalRoundTrip_OKX(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if os.Getenv("OKX_INTEGRATION") == "" {
		t.Skip("OKX integration tests disabled — set OKX_INTEGRATION=1 to enable")
	}
	if okxPaperAPIKey == "" {
		t.Skip("OKX paper credentials not set in creds_test.go")
	}

	const symbol = "BTC-USDT"
	ex := okxact.New(okxact.Config{Paper: true})
	creds := exchange.Credentials{APIKey: okxPaperAPIKey, APISecret: okxPaperAPISecret, Passphrase: okxPaperPassphrase}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.MarketData.SetPrice(symbol, price)
		t.Logf("current %s price: %s", symbol, price)
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer func() {
		time.Sleep(5 * time.Second)
		logHandState(t, rt, hand, symbol)
		hand.Stop()
	}()

	// Entry.
	fillEvts1 := hand.Subscribe(64)
	entryNotify := orderNotify(hand, 15*time.Second)
	hand.DeliverSignal(longSig(symbol))
	var entryOrderID string
	select {
	case entry := <-entryNotify:
		t.Logf("entry: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		entryOrderID = entry.OrderID
		if entry.Code == eventcode.CodeOrderFailed {
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
			t.Fatalf("entry order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for entry order")
	}

	if !waitFillsCh(fillEvts1, 1, 20*time.Second) {
		t.Fatal("timeout waiting for entry fill")
	}

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected open long position after entry fill")
	}
	t.Logf("position after entry: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Exit. Use captured entryOrderID — pollOrders prunes filled orders from h.orders.
	fillEvts2 := hand.Subscribe(64)
	exitNotify := orderNotifyNew(hand, entryOrderID, 15*time.Second)
	hand.DeliverSignal(exitSig(symbol))
	select {
	case entry := <-exitNotify:
		t.Logf("exit: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == eventcode.CodeOrderFailed {
			t.Logf("exit order failed (non-fatal for sandbox): %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for exit order")
	}

	if !waitFillsCh(fillEvts2, 2, 20*time.Second) {
		t.Log("exit fill not confirmed within 20s — order may still be pending")
	}

	cancelAllOrders(t, rt, hand)
}

// ── Bybit Demo — round-trip ───────────────────────────────────────────────────

func TestSignalRoundTrip_Bybit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if bybitTestAPIKey == "" {
		t.Skip("Bybit demo credentials not set in creds_test.go")
	}

	const symbol = "BTCUSDT"
	ex := bybitact.New(bybitact.Config{Paper: true})
	creds := exchange.Credentials{APIKey: bybitTestAPIKey, APISecret: bybitTestAPISecret}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.MarkPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.MarketData.SetPrice(symbol, price)
		t.Logf("current %s mark price: %s", symbol, price)
	} else {
		rt.MarketData.SetPrice(symbol, decimal.NewFromFloat(65_000))
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(0.001))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer func() {
		time.Sleep(5 * time.Second)
		logHandState(t, rt, hand, symbol)
		hand.Stop()
	}()

	// Entry.
	fillEvts1 := hand.Subscribe(64)
	entryNotify := orderNotify(hand, 15*time.Second)
	hand.DeliverSignal(longSig(symbol))
	var entryOrderID string
	select {
	case entry := <-entryNotify:
		t.Logf("entry: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		entryOrderID = entry.OrderID
		if entry.Code == eventcode.CodeOrderFailed {
			if isBalanceError(entry.Reason) {
				t.Skipf("sandbox needs top-up: %s", entry.Reason)
			}
			t.Fatalf("entry order failed: %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for entry order")
	}

	if !waitFillsCh(fillEvts1, 1, 20*time.Second) {
		t.Fatal("timeout waiting for entry fill")
	}

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected open long position after entry fill")
	}
	t.Logf("position after entry: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Exit. Use captured entryOrderID — pollOrders prunes filled orders from h.orders.
	fillEvts2 := hand.Subscribe(64)
	exitNotify := orderNotifyNew(hand, entryOrderID, 15*time.Second)
	hand.DeliverSignal(exitSig(symbol))
	select {
	case entry := <-exitNotify:
		t.Logf("exit: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == eventcode.CodeOrderFailed {
			t.Logf("exit order failed (non-fatal for sandbox): %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for exit order")
	}

	if !waitFillsCh(fillEvts2, 2, 20*time.Second) {
		t.Log("exit fill not confirmed within 20s — order may still be pending")
	}

	cancelAllOrders(t, rt, hand)
}

// ── Alpaca Paper — round-trip ─────────────────────────────────────────────────

func TestSignalRoundTrip_Alpaca(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if alpacaPaperAPIKey == "" {
		t.Skip("Alpaca paper credentials not set in creds_test.go")
	}

	const symbol = "AAPL"
	ex := alpacaact.New(alpacaact.Config{})
	creds := exchange.Credentials{APIKey: alpacaPaperAPIKey, APISecret: alpacaPaperAPISecret}

	rt := newTestRuntime(ex, creds, decimal.NewFromFloat(100_000))
	defer rt.Stop()

	ctx10s, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, symbol); err == nil && price.IsPositive() {
		rt.MarketData.SetPrice(symbol, price)
		t.Logf("current %s price: %s", symbol, price)
	}

	hand := newFixedQtyHand(rt, decimal.NewFromFloat(1))
	hand.Symbol = symbol
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer func() {
		time.Sleep(5 * time.Second)
		logHandState(t, rt, hand, symbol)
		hand.Stop()
	}()

	// Entry.
	fillEvts1 := hand.Subscribe(64)
	entryNotify := orderNotify(hand, 15*time.Second)
	hand.DeliverSignal(longSig(symbol))
	var entryOrderID string
	select {
	case entry := <-entryNotify:
		t.Logf("entry: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		entryOrderID = entry.OrderID
		if entry.Code == eventcode.CodeOrderFailed {
			t.Logf("entry not placed (market may be closed): %s", entry.Reason)
			return
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for entry order")
	}

	if !waitFillsCh(fillEvts1, 1, 30*time.Second) {
		t.Log("entry fill not confirmed within 30s — market may be closed, cancelling")
		cancelAllOrders(t, rt, hand)
		return
	}

	pos := rt.Portfolio.GetPosition(symbol)
	if pos == nil || !pos.Qty.IsPositive() {
		t.Fatal("expected open long position after entry fill")
	}
	t.Logf("position after entry: qty=%s avg_px=%s", pos.Qty, pos.AvgPrice)

	// Exit. Use captured entryOrderID — pollOrders prunes filled orders from h.orders.
	fillEvts2 := hand.Subscribe(64)
	exitNotify := orderNotifyNew(hand, entryOrderID, 15*time.Second)
	hand.DeliverSignal(exitSig(symbol))
	select {
	case entry := <-exitNotify:
		t.Logf("exit: code=%d order_id=%s qty=%s price=%s reason=%s",
			entry.Code, entry.OrderID, entry.Qty, entry.Price, entry.Reason)
		if entry.Code == eventcode.CodeOrderFailed {
			t.Logf("exit order failed (non-fatal): %s", entry.Reason)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for exit order")
	}

	if !waitFillsCh(fillEvts2, 2, 30*time.Second) {
		t.Log("exit fill not confirmed within 30s")
	}

	cancelAllOrders(t, rt, hand)
}
