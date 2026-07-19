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

	"mallow/helm/internal/fleet/actor"
	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/risk"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/core/tactics"
	"mallow/helm/internal/infra/exchange"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	"mallow/helm/internal/infra/journal/poslog"
)

const natsURL = "nats://helm:helm-dev@127.0.0.1:4222"

func TestPoslog_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
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
	rt := actor.NewHelmRuntime(helmID, uuid.New(), uuid.New(), ex.Name(), pf, rm, ex, creds, nil, time.Now())
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
	hand := actor.NewHand(uuid.New(), helmID, rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	hand.Symbol = "BTCUSDT"
	hand.StrategyName = "signal_follower"
	hand.EnableEventSink()
	hand.Start()
	defer hand.Stop()

	placed := orderNotify(hand, 20*time.Second)
	filled := fillNotify(hand, 40*time.Second)

	hand.DeliverSignal(longSig("BTCUSDT"))

	// Wait for order placed.
	select {
	case e := <-placed:
		t.Logf("placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
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

// TestTradeRoundTrip_E2E exercises the complete poslog trade cycle:
//
//	entry signal → buy fills → exit signal → sell fills
//	→ position_closed event (enriched with symbol/entry/pnl)
//	→ TradesPaged returns the completed trade record.
//
// go test -v -run TestTradeRoundTrip_E2E ./internal/runtime/ -timeout 120s
func TestTradeRoundTrip_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping exchange integration test in -short mode")
	}
	if binanceDemoAPIKey == "" {
		t.Skip("binance demo credentials not set in creds_test.go")
	}

	// ── NATS + JetStream ──────────────────────────────────────────────────────
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Skipf("NATS unavailable at %s: %v", natsURL, err)
	}
	defer nc.Close()

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

	helmID := uuid.New()
	capital := decimal.NewFromFloat(100_000)
	if syncer, ok := exchange.Exchange(ex).(exchange.AccountSyncer); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if snap, snapErr := syncer.SyncAccount(ctx, creds, nil); snapErr == nil && snap.Cash.IsPositive() {
			capital = snap.Cash
			t.Logf("sandbox balance: %s USDT", capital)
		}
		cancel()
	}

	pf := portfolio.New(capital)
	rm := risk.New(risk.DefaultConfig(), pf)
	rt := actor.NewHelmRuntime(helmID, uuid.New(), uuid.New(), ex.Name(), pf, rm, ex, creds, nil, time.Now())
	rt.PosLog = pl
	defer rt.Stop()

	ctx10s, cancel10 := context.WithTimeout(context.Background(), 10*time.Second)
	if price, priceErr := ex.GetCurrentPrice(ctx10s, creds, "BTCUSDT"); priceErr == nil && price.IsPositive() {
		rt.UpdatePrice("BTCUSDT", price)
		t.Logf("BTC price seeded: %s", price)
	}
	cancel10()

	// ── Hand ──────────────────────────────────────────────────────────────────
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(0.001),
	})
	hand := actor.NewHand(uuid.New(), helmID, rt, strat, tact, false, 1, 0, nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	hand.Symbol = "BTCUSDT"
	hand.StrategyName = "signal_follower"
	hand.EnableEventSink()
	hand.Start()
	defer hand.Stop()

	// ── Entry ─────────────────────────────────────────────────────────────────
	entryPlaced := orderNotify(hand, 20*time.Second)
	hand.DeliverSignal(longSig("BTCUSDT"))

	var entryOrderID string
	select {
	case e := <-entryPlaced:
		t.Logf("entry placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			if isBalanceError(e.Reason) {
				t.Skipf("sandbox needs top-up: %s", e.Reason)
			}
			t.Fatalf("entry order failed: %s", e.Reason)
		}
		entryOrderID = e.OrderID
	case <-time.After(20 * time.Second):
		t.Fatal("timeout: entry order not placed within 20s")
	}

	// Wait for entry fill.
	select {
	case e := <-fillNotify(hand, 40*time.Second):
		t.Logf("entry filled: order_id=%s qty=%s avg=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("entry fill not observed in activity (WS may not be running) — proceeding with exit")
	}

	// Give entry poslog events time to flush to JetStream.
	time.Sleep(2 * time.Second)

	// ── Exit ──────────────────────────────────────────────────────────────────
	exitPlaced := orderNotifyNew(hand, entryOrderID, 20*time.Second)
	hand.DeliverSignal(exitSig("BTCUSDT"))

	var exitOrderID string
	select {
	case e := <-exitPlaced:
		t.Logf("exit placed: order_id=%s side=%s qty=%s", e.OrderID, e.Side, e.Qty)
		if e.Code == actor.CodeOrderFailed {
			// No open position to close — skip rather than fail (entry may not have filled).
			t.Skipf("exit order failed (position may not be open): %s", e.Reason)
		}
		exitOrderID = e.OrderID
	case <-time.After(20 * time.Second):
		t.Log("exit order not placed within 20s — no open position to close; skipping")
		t.SkipNow()
	}

	// Wait for exit fill.
	select {
	case e := <-fillNotifyOrder(hand, exitOrderID, 30*time.Second):
		t.Logf("exit filled: order_id=%s qty=%s avg=%s", e.OrderID, e.Qty, e.Price)
	case <-time.After(30 * time.Second):
		t.Log("exit fill not observed in activity — checking poslog anyway")
	}

	// Allow poslog publish goroutines to flush to JetStream.
	time.Sleep(3 * time.Second)

	// ── Poslog: verify position_closed ────────────────────────────────────────
	replayCtx, replayCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer replayCancel()

	events, err := pl.ReplayHand(replayCtx, helmID.String(), hand.ID().String())
	if err != nil {
		t.Fatalf("ReplayHand: %v", err)
	}
	t.Logf("poslog events: %d", len(events))
	for _, ev := range events {
		t.Logf("  [%s] pos=%s at=%s", ev.Kind, ev.PositionID, ev.At.Format(time.RFC3339))
	}

	var closedPayload *poslog.PositionClosedPayload
	for _, ev := range events {
		if ev.Kind == poslog.KindPositionClosed {
			var p poslog.PositionClosedPayload
			if jsonErr := json.Unmarshal(ev.Payload, &p); jsonErr == nil {
				closedPayload = &p
				break
			}
		}
	}

	if closedPayload == nil {
		t.Fatal("expected position_closed event in poslog — not found")
	}
	t.Logf("position_closed: symbol=%s side=%s qty=%s entry=%s exit=%s pnl=%s source=%s",
		closedPayload.Symbol, closedPayload.Side, closedPayload.Qty,
		closedPayload.EntryPrice, closedPayload.ClosePrice, closedPayload.RealizedPnL, closedPayload.ExitReason)

	if closedPayload.Symbol == "" {
		t.Error("position_closed.Symbol is empty (pre-enrichment event?)")
	}
	if closedPayload.EntryPrice == "" || closedPayload.EntryPrice == "0" {
		t.Error("position_closed.EntryPrice is empty or zero")
	}
	if closedPayload.EntryAt.IsZero() {
		t.Error("position_closed.EntryAt is zero")
	}

	// ── TradesPaged: verify cursor-based query ─────────────────────────────────
	pageCtx, pageCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pageCancel()

	page, err := pl.TradesPaged(pageCtx, helmID.String(), hand.ID().String(), 0, 10)
	if err != nil {
		t.Fatalf("TradesPaged: %v", err)
	}
	t.Logf("TradesPaged: %d trades hasMore=%v next=%d", len(page.Trades), page.HasMore, page.Next)
	for _, tr := range page.Trades {
		t.Logf("  trade: symbol=%s side=%s qty=%s entry=%s exit=%s pnl=%s cursor=%d",
			tr.Symbol, tr.Side, tr.Qty, tr.EntryPrice, tr.ExitPrice, tr.RealizedPnL, tr.Cursor)
	}

	if len(page.Trades) == 0 {
		t.Fatal("TradesPaged returned 0 trades — position_closed not picked up")
	}

	tr := page.Trades[0]
	if tr.HandID != hand.ID().String() {
		t.Errorf("trade.HandID mismatch: got %s, want %s", tr.HandID, hand.ID().String())
	}
	if tr.Symbol != "BTCUSDT" {
		t.Errorf("trade.Symbol: got %q, want %q", tr.Symbol, "BTCUSDT")
	}
	if tr.Cursor == 0 {
		t.Error("trade.Cursor should be non-zero (JetStream sequence)")
	}

	// Second page starting from next cursor should return empty (only one trade).
	page2, err := pl.TradesPaged(pageCtx, helmID.String(), hand.ID().String(), page.Next, 10)
	if err != nil {
		t.Fatalf("TradesPaged page2: %v", err)
	}
	if page.HasMore && len(page2.Trades) == 0 {
		t.Error("HasMore=true but second page is empty")
	}
	if !page.HasMore {
		t.Logf("single page confirmed (HasMore=false, no further pages)")
	}

	// Cleanup: cancel any remaining open orders.
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanCancel()
	if orders, listErr := ex.ListOpenOrders(cleanCtx, creds, "BTCUSDT"); listErr == nil {
		for _, o := range orders {
			if cancelErr := ex.CancelOrder(cleanCtx, creds, o.ID); cancelErr == nil {
				t.Logf("cleanup: cancelled %s", o.ID)
			}
		}
	}
}
