package runtime_test

// snapshot_test.go — verifies BuildSnapshot returns correct helm-level state after fills.
//
// No NATS, no real exchange — all fakes.
//
// go test -v -run TestSnapshot ./internal/runtime/ -count=1

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func buildSnapshotRuntime(ex *simExchange, capital float64) *runtime.HelmRuntime {
	pf := portfolio.New(decimal.NewFromFloat(capital))
	cfg := risk.Config{MaxPositions: 10, DailyLossLimitPct: 0.5, MaxDrawdownPct: 0.5}
	rm := risk.New(cfg, pf)
	rt := runtime.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		"sim", pf, rm, ex, exchange.Credentials{}, nil, time.Now(),
	)
	rm.SetUnitCounter(rt.OpenUnitCount)
	rt.StartFillStreaming(context.Background())
	return rt
}

// addAllocatedHand creates a Hand with FixedQty + an allocated capital budget,
// so hand-level position sizing computes meaningful values.
func addAllocatedHand(rt *runtime.HelmRuntime, symbol string, qty, allocatedCap float64) *runtime.Hand {
	strat := strategy.NewSignalFollower(0.0)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(qty),
	})
	h := runtime.NewHand(
		uuid.New(), rt.HelmID, rt,
		strat, tact,
		false, 5, 0,
		nil, domain.OrderTypeMarket, 0, domain.LimitFallbackCancel,
		domain.HandGuardConfig{}, decimal.NewFromFloat(allocatedCap),
	)
	h.Symbol = symbol
	h.StrategyName = "signal_follower"
	h.EnableEventSink()
	rt.AddHand(h)
	return h
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestSnapshot_SignalToFill verifies that after a fill, BuildSnapshot returns
// a helm-level snapshot with the correct positions, cash, and equity.
func TestSnapshot_SignalToFill(t *testing.T) {
	const (
		symbol    = "BTCUSDT"
		fillPrice = 50_000.0
		orderQty  = 0.01 // BTC
		capital   = 100_000.0
		allocCap  = 5_000.0
	)

	ex := newSim(fillPrice)
	rt := buildSnapshotRuntime(ex, capital)
	defer rt.Stop()

	rt.UpdatePrice(symbol, decimal.NewFromFloat(fillPrice))

	hand := addAllocatedHand(rt, symbol, orderQty, allocCap)
	hand.Start()
	defer hand.Stop()

	hand.DeliverSignal(longSignalFor(symbol))

	filled, ok := waitCode(hand, runtime.CodeOrderFilled, 2*time.Second)
	if !ok {
		t.Fatal("timeout: CodeOrderFilled did not appear within 2s")
	}
	t.Logf("fill: order_id=%s symbol=%s side=%s qty=%s price=%s",
		filled.OrderID, filled.Symbol, filled.Side, filled.Qty, filled.Price)

	// BuildSnapshot reads the in-memory portfolio state — no async needed.
	snap := rt.BuildSnapshot(time.Now())
	if snap == nil {
		t.Fatal("BuildSnapshot returned nil")
	}
	t.Logf("helm snapshot: cash=%s equity=%s positions=%d", snap.Cash, snap.Equity, len(snap.Positions))

	if snap.HelmID != rt.HelmID.String() {
		t.Errorf("snapshot HelmID mismatch: want %s, got %s", rt.HelmID, snap.HelmID)
	}

	// Should have an open position.
	if len(snap.Positions) == 0 {
		t.Error("helm snapshot: expected ≥1 position after fill, got 0")
	} else {
		pos := snap.Positions[0]
		if pos.Symbol != symbol {
			t.Errorf("helm snapshot position symbol: want %s, got %s", symbol, pos.Symbol)
		}
		expectedQty := decimal.NewFromFloat(orderQty)
		if !pos.Qty.Equal(expectedQty) {
			t.Errorf("helm snapshot position qty: want %s, got %s", expectedQty, pos.Qty)
		}
		t.Logf("helm position: symbol=%s qty=%s avg_price=%s", pos.Symbol, pos.Qty, pos.AvgPrice)
	}

	// Cash decreases by the cost of the position (qty × price).
	cost := decimal.NewFromFloat(orderQty * fillPrice)
	expectedCash := decimal.NewFromFloat(capital).Sub(cost)
	if !snap.Cash.Equal(expectedCash) {
		t.Errorf("helm snapshot cash: want %s, got %s", expectedCash, snap.Cash)
	}

	// Equity ≈ capital (flat PnL at fill price: cash + market value = initial capital).
	expectedEquity := decimal.NewFromFloat(capital)
	if !snap.Equity.Equal(expectedEquity) {
		t.Errorf("helm snapshot equity: want %s (unchanged at fill price), got %s", expectedEquity, snap.Equity)
	}

	// MarkSnapshotDirty should set the flag (picked up by SnapshotWorker within 500ms).
	rt.MarkSnapshotDirty()
}

// TestSnapshot_RoundTrip_EntryThenExit verifies that after an entry + exit fill,
// the helm snapshot shows no open positions and equity is approximately restored.
func TestSnapshot_RoundTrip_EntryThenExit(t *testing.T) {
	const (
		symbol    = "BTCUSDT"
		fillPrice = 50_000.0
		orderQty  = 0.01
		capital   = 100_000.0
		allocCap  = 5_000.0
	)

	ex := newSim(fillPrice)
	rt := buildSnapshotRuntime(ex, capital)
	defer rt.Stop()

	rt.UpdatePrice(symbol, decimal.NewFromFloat(fillPrice))

	hand := addAllocatedHand(rt, symbol, orderQty, allocCap)
	hand.Start()
	defer hand.Stop()

	// Entry.
	hand.DeliverSignal(longSignalFor(symbol))
	if _, ok := waitCode(hand, runtime.CodeOrderFilled, 2*time.Second); !ok {
		t.Fatal("timeout: entry fill not observed")
	}

	// Exit.
	hand.DeliverSignal(exitSignalFor(symbol))
	waitCode(hand, runtime.CodeOrderFilled, 3*time.Second)

	snap := rt.BuildSnapshot(time.Now())
	if snap == nil {
		t.Fatal("BuildSnapshot returned nil after exit")
	}
	t.Logf("helm snapshot after exit: cash=%s equity=%s positions=%d",
		snap.Cash, snap.Equity, len(snap.Positions))

	if len(snap.Positions) != 0 {
		t.Errorf("after exit: helm snapshot positions: want 0, got %d", len(snap.Positions))
	}
	// Equity should be ~capital (buy and sell at same price → flat PnL).
	if !snap.Equity.Equal(decimal.NewFromFloat(capital)) {
		t.Logf("helm equity after round-trip: %s (expected ~%v)", snap.Equity, capital)
	}
}

// TestAvailableCash verifies that available cash is properly computed and segregated:
//   - Hand allocations are subtracted from total cash to get AvailableCash.
//   - Hand trades (fills) do not affect AvailableCash.
//   - Manual trades (fills with no Hand ID) do affect AvailableCash.
func TestAvailableCash(t *testing.T) {
	const (
		symbol    = "BTCUSDT"
		fillPrice = 50_000.0
		orderQty  = 0.01 // $500 cost
		capital   = 10_000.0
		allocCap  = 3_000.0
	)

	ex := newSim(fillPrice)
	rt := buildSnapshotRuntime(ex, capital)
	defer rt.Stop()

	// Initial State: total cash = 10,000, no hands.
	if !rt.AvailableCash().Equal(decimal.NewFromFloat(capital)) {
		t.Errorf("expected available cash %f, got %s", capital, rt.AvailableCash())
	}

	rt.UpdatePrice(symbol, decimal.NewFromFloat(fillPrice))

	// Add hand with allocated capital: 3,000
	// Available cash should be 10,000 - 3,000 = 7,000
	hand := addAllocatedHand(rt, symbol, orderQty, allocCap)
	hand.Start()
	defer hand.Stop()

	expectedAvailable := decimal.NewFromFloat(capital - allocCap)
	if !rt.AvailableCash().Equal(expectedAvailable) {
		t.Errorf("expected available cash after allocation %s, got %s", expectedAvailable, rt.AvailableCash())
	}

	// Deliver a hand trade entry signal.
	// buy BTC: total cash decreases by $500 (to 9500), hand deployed = 500
	// hand logical cash = allocated (3000) + PnL (0) - deployed (500) = 2500
	// available cash = total cash (9500) - hand logical cash (2500) = 7000
	hand.DeliverSignal(longSignalFor(symbol))
	if _, ok := waitCode(hand, runtime.CodeOrderFilled, 2*time.Second); !ok {
		t.Fatal("timeout: hand fill not observed")
	}

	if !rt.AvailableCash().Equal(expectedAvailable) {
		t.Errorf("expected available cash after hand trade fill to remain %s, got %s",
			expectedAvailable, rt.AvailableCash())
	}

	// Deliver a manual trade fill (no HandID).
	// Manual buy of $1000: total cash decreases to 8500.
	// available cash = total cash (8500) - hand logical cash (2500) = 6000
	rt.ReportFill(helmdomain.FillReport{
		HandID:    "", // manual
		HelmID:    rt.HelmID.String(),
		OrderID:   "manual-order-1",
		Symbol:    symbol,
		Side:      "buy",
		Qty:       decimal.NewFromFloat(0.02), // $1000 cost
		Price:     decimal.NewFromFloat(fillPrice),
		Timestamp: time.Now().UTC(),
	})

	expectedAvailableManual := expectedAvailable.Sub(decimal.NewFromFloat(1000))
	if !rt.AvailableCash().Equal(expectedAvailableManual) {
		t.Errorf("expected available cash after manual trade fill %s, got %s",
			expectedAvailableManual, rt.AvailableCash())
	}

	// Remove the hand — reclaims its allocated budget.
	// Total cash = 8500. No active hands → available = total.
	rt.RemoveHand(hand.ID().String())

	if !rt.AvailableCash().Equal(rt.Cash()) {
		t.Errorf("expected available cash after hand removal to equal total cash %s, got %s",
			rt.Cash(), rt.AvailableCash())
	}
}
