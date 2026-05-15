// Integration test: signal → order → poslog events.
//
// Wires a real JetStream-backed NatsLog into HelmRuntime and verifies that
// order_placed and order_filled events are written to the HELM_POSITIONS stream.
//
// Requirements:
//   - NATS with JetStream running at nats://helm:helm-dev@127.0.0.1:4222
//   - Binance demo credentials in creds_test.go
//
// go test -v -run TestPoslog_E2E ./internal/runtime/ -timeout 90s
package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

const natsURL = "nats://helm:helm-dev@127.0.0.1:4222"

func TestPoslog_E2E(t *testing.T) {
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set in creds_test.go")
	}

	// ── NATS + JetStream ──────────────────────────────────────────────────────
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Skipf("NATS unavailable at %s: %v", natsURL, err)
	}
	defer nc.Close()
	t.Logf("NATS connected: %s", natsURL)

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}

	pl, err := poslog.NewNatsLog(js)
	if err != nil {
		t.Fatalf("NewNatsLog: %v", err)
	}

	// ── HelmRuntime ───────────────────────────────────────────────────────────
	ex := binanceact.New(true)
	creds := exchange.Credentials{APIKey: binanceDemoAPIKey, APISecret: binanceDemoAPISecret}

	ctx15s, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	capital := decimal.NewFromFloat(100_000)
	if syncer, ok := exchange.Exchange(ex).(exchange.AccountSyncer); ok {
		if snap, err := syncer.SyncAccount(ctx15s, creds, nil); err == nil && snap.Cash.IsPositive() {
			capital = snap.Cash
			t.Logf("sandbox balance: %s USDT", capital)
		}
	}

	helmID := uuid.New()
	pf := portfolio.New(capital)
	rm := risk.New(risk.DefaultConfig(), pf)
	ob := orderbook.NewOrderBook(ex.Name())
	rt := runtime.NewHelmRuntime(helmID, uuid.New(), uuid.New(), ex.Name(), pf, rm, ob, ex, creds, nil)
	rt.PosLog = pl
	defer rt.Stop()

	ctx10s, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	if price, err := ex.GetCurrentPrice(ctx10s, creds, "BTCUSDT"); err == nil && price.IsPositive() {
		rt.UpdatePrice("BTCUSDT", price)
		t.Logf("BTC price: %s", price)
	}

	// ── Hand ──────────────────────────────────────────────────────────────────
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(0.001),
	})
	hand := runtime.NewHand(uuid.New(), helmID, rt, strat, tact, false, 1, 0, nil)
	hand.Symbol = "BTCUSDT"
	hand.StrategyName = "signal_follower"
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 30*time.Second)

	hand.DeliverSignal(longSig("BTCUSDT"))

	// Wait for order placed.
	select {
	case e := <-placed:
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == runtime.CodeOrderFailed {
			t.Fatalf("order failed: %s", e.Reason)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: order not placed within 20s")
	}

	// Wait for fill (may not arrive if WS not streaming, that's okay).
	select {
	case e := <-filled:
		t.Logf("filled: order_id=%s qty=%s avg=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(15 * time.Second):
		t.Log("fill not observed via activity — checking poslog anyway")
	}

	// Give publishOrderPlaced / publishOrderFilled goroutines time to flush.
	time.Sleep(2 * time.Second)

	// ── Replay poslog and assert events ───────────────────────────────────────
	replayCtx, replayCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer replayCancel()

	events, err := pl.ReplayHand(replayCtx, helmID.String(), hand.ID().String())
	if err != nil {
		t.Fatalf("ReplayHand: %v", err)
	}

	t.Logf("poslog events replayed: %d", len(events))
	for _, ev := range events {
		t.Logf("  [%s] id=%s pos=%s at=%s", ev.Kind, ev.ID, ev.PositionID, ev.At.Format(time.RFC3339))
	}

	if len(events) == 0 {
		t.Fatal("no poslog events written — PosLog may not be wired or Publish failed")
	}

	hasPlaced := false
	hasFilled := false
	for _, ev := range events {
		switch ev.Kind {
		case poslog.KindOrderPlaced:
			hasPlaced = true
		case poslog.KindOrderFilled:
			hasFilled = true
		}
	}

	if !hasPlaced {
		t.Error("expected order_placed event in poslog, not found")
	}
	// order_filled may not be present if fill arrived after replay window — log only.
	if hasFilled {
		t.Log("order_filled event confirmed in poslog")
	} else {
		t.Log("order_filled not yet in poslog (fill may still be pending at exchange)")
	}

	// Cleanup open orders.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	if orders, err := ex.ListOpenOrders(cleanupCtx, creds, "BTCUSDT"); err == nil {
		for _, o := range orders {
			if err := ex.CancelOrder(cleanupCtx, creds, o.ID); err == nil {
				t.Logf("cleanup: cancelled %s", o.ID)
			}
		}
	}
}
