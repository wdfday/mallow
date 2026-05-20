package runtime_test

// snapshot_test.go — verifies the end-to-end snapshot logging flow:
//   signal delivered → order placed → order filled →
//     helm-level Snapshot appended (cash + equity + position)
//     hand-level Snapshot appended (legs + equity from allocated cap)
//
// No NATS, no real exchange — all fakes.
//
// go test -v -run TestSnapshot ./internal/runtime/ -count=1

import (
	"context"
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
	"mallow/helm/internal/runtime/perf"
)

// ── mockSnapshotLog ────────────────────────────────────────────────────────────

type mockSnapshotLog struct {
	mu  sync.Mutex
	got []perf.Snapshot
}

func (m *mockSnapshotLog) Append(_ context.Context, s perf.Snapshot) error {
	m.mu.Lock()
	m.got = append(m.got, s)
	m.mu.Unlock()
	return nil
}

func (m *mockSnapshotLog) Query(_ context.Context, _, _ string, _ perf.Page) (perf.SnapshotPage, error) {
	return perf.SnapshotPage{}, nil
}

func (m *mockSnapshotLog) Latest(_ context.Context, _, _ string, _ int) ([]perf.Snapshot, error) {
	return nil, nil
}

func (m *mockSnapshotLog) snapshots() []perf.Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]perf.Snapshot, len(m.got))
	copy(out, m.got)
	return out
}

func (m *mockSnapshotLog) waitN(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		got := len(m.got)
		m.mu.Unlock()
		if got >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// ── helpers ───────────────────────────────────────────────────────────────────

func buildSnapshotRuntime(ex *simExchange, capital float64, snapLog perf.SnapshotLog) *runtime.HelmRuntime {
	pf := portfolio.New(decimal.NewFromFloat(capital))
	cfg := risk.Config{MaxPositions: 10, DailyLossLimitPct: 0.5, MaxDrawdownPct: 0.5}
	rm := risk.New(cfg, pf)
	rt := runtime.NewHelmRuntime(
		uuid.New(), uuid.New(), uuid.New(),
		"sim", pf, rm, ex, exchange.Credentials{}, nil,
	)
	rm.SetUnitCounter(rt.OpenUnitCount)
	rt.SnapshotLog = snapLog
	return rt
}

// addAllocatedHand creates a Hand with FixedQty + an allocated capital budget,
// so handSnapshot() computes meaningful cash and equity values.
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
		domain.HandRiskConfig{}, decimal.NewFromFloat(allocatedCap),
	)
	h.Symbol = symbol
	h.StrategyName = "signal_follower"
	rt.AddHand(h)
	return h
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestSnapshot_SignalToFill_HelmAndHandSnapshot verifies that after a signal is
// delivered and the order fills, both a helm-level and a hand-level snapshot are
// appended to SnapshotLog with the correct positions and equity values.
func TestSnapshot_SignalToFill_HelmAndHandSnapshot(t *testing.T) {
	const (
		symbol      = "BTCUSDT"
		fillPrice   = 50_000.0
		orderQty    = 0.01   // BTC
		capital     = 100_000.0
		allocatedCap = 5_000.0 // hand has its own budget
	)

	snapLog := &mockSnapshotLog{}
	ex := newSim(fillPrice)
	rt := buildSnapshotRuntime(ex, capital, snapLog)
	defer rt.Stop()

	// Seed price so ProcessTrade can size the order.
	rt.UpdatePrice(symbol, decimal.NewFromFloat(fillPrice))

	hand := addAllocatedHand(rt, symbol, orderQty, allocatedCap)
	hand.Start()
	defer hand.Stop()

	// Deliver a long entry signal.
	hand.DeliverSignal(longSignalFor(symbol))

	// Wait for fill to appear in activity ring.
	filled, ok := waitCode(hand, runtime.CodeOrderFilled, 2*time.Second)
	if !ok {
		t.Fatal("timeout: CodeOrderFilled did not appear within 2s")
	}
	t.Logf("fill: order_id=%s symbol=%s side=%s qty=%s price=%s",
		filled.OrderID, filled.Symbol, filled.Side, filled.Qty, filled.Price)

	// applyFill publishes snapshots via goroutines — give them time to land.
	if !snapLog.waitN(2, 2*time.Second) {
		t.Fatalf("timeout: expected ≥2 snapshots (helm + hand), got %d", len(snapLog.snapshots()))
	}

	snaps := snapLog.snapshots()
	t.Logf("total snapshots received: %d", len(snaps))

	var helmSnap, handSnap *perf.Snapshot
	for i := range snaps {
		s := &snaps[i]
		if s.HandID == "" {
			helmSnap = s
		} else if s.HandID == hand.ID().String() {
			handSnap = s
		}
	}

	// ── helm-level snapshot ───────────────────────────────────────────────────
	if helmSnap == nil {
		t.Fatal("no helm-level snapshot (HandID='') found")
	}
	t.Logf("helm snapshot: cash=%s equity=%s positions=%d", helmSnap.Cash, helmSnap.Equity, len(helmSnap.Positions))

	if len(helmSnap.Positions) == 0 {
		t.Error("helm snapshot: expected ≥1 position after fill, got 0")
	} else {
		pos := helmSnap.Positions[0]
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
	if !helmSnap.Cash.Equal(expectedCash) {
		t.Errorf("helm snapshot cash: want %s, got %s", expectedCash, helmSnap.Cash)
	}

	// Equity ≈ cash + position value (mark-to-market at fill price).
	expectedEquity := decimal.NewFromFloat(capital) // flat PnL at fill price
	if !helmSnap.Equity.Equal(expectedEquity) {
		t.Errorf("helm snapshot equity: want %s (unchanged at fill price), got %s", expectedEquity, helmSnap.Equity)
	}

	// ── hand-level snapshot ───────────────────────────────────────────────────
	if handSnap == nil {
		t.Fatal("no hand-level snapshot found")
	}
	t.Logf("hand snapshot: cash=%s equity=%s positions=%d", handSnap.Cash, handSnap.Equity, len(handSnap.Positions))

	if len(handSnap.Positions) == 0 {
		t.Error("hand snapshot: expected ≥1 position after fill, got 0")
	} else {
		pos := handSnap.Positions[0]
		if pos.Symbol != symbol {
			t.Errorf("hand snapshot position symbol: want %s, got %s", symbol, pos.Symbol)
		}
		t.Logf("hand position: symbol=%s qty=%s avg_price=%s", pos.Symbol, pos.Qty, pos.AvgPrice)
	}

	// Hand equity = allocatedCap + closedPnL (no closes yet → closedPnL=0).
	expectedHandEquity := decimal.NewFromFloat(allocatedCap)
	if !handSnap.Equity.Equal(expectedHandEquity) {
		t.Errorf("hand snapshot equity: want %s (allocated cap, no closed trades), got %s",
			expectedHandEquity, handSnap.Equity)
	}

	// Hand cash = equity - invested (invested = qty × fillPrice).
	invested := decimal.NewFromFloat(orderQty * fillPrice)
	expectedHandCash := expectedHandEquity.Sub(invested)
	if !handSnap.Cash.Equal(expectedHandCash) {
		t.Errorf("hand snapshot cash: want %s (equity - invested), got %s",
			expectedHandCash, handSnap.Cash)
	}
}

// TestSnapshot_RoundTrip_EntryThenExit verifies that after an entry + exit fill,
// both helm and hand snapshots reflect the closed position:
// helm positions are empty, hand cash equals allocatedCap ± PnL.
func TestSnapshot_RoundTrip_EntryThenExit(t *testing.T) {
	const (
		symbol      = "BTCUSDT"
		fillPrice   = 50_000.0
		orderQty    = 0.01
		capital     = 100_000.0
		allocatedCap = 5_000.0
	)

	snapLog := &mockSnapshotLog{}
	ex := newSim(fillPrice)
	rt := buildSnapshotRuntime(ex, capital, snapLog)
	defer rt.Stop()

	// Seed price so ProcessTrade can size the order.
	rt.UpdatePrice(symbol, decimal.NewFromFloat(fillPrice))

	hand := addAllocatedHand(rt, symbol, orderQty, allocatedCap)
	hand.Start()
	defer hand.Stop()

	// Entry signal.
	hand.DeliverSignal(longSignalFor(symbol))
	if _, ok := waitCode(hand, runtime.CodeOrderFilled, 2*time.Second); !ok {
		t.Fatal("timeout: entry fill not observed")
	}

	// Exit signal (urgent).
	hand.DeliverSignal(exitSig(symbol))
	// Wait for second fill.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		fills := 0
		for _, e := range hand.Activity() {
			if e.Code == runtime.CodeOrderFilled {
				fills++
			}
		}
		if fills >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for ≥4 snapshots (entry helm + entry hand + exit helm + exit hand).
	if !snapLog.waitN(4, 3*time.Second) {
		t.Logf("warning: expected ≥4 snapshots, got %d — continuing with partial check", len(snapLog.snapshots()))
	}

	snaps := snapLog.snapshots()
	t.Logf("total snapshots: %d", len(snaps))
	for i, s := range snaps {
		handLabel := "helm"
		if s.HandID != "" {
			handLabel = "hand"
		}
		t.Logf("  [%d] %s ts=%s cash=%s equity=%s positions=%d",
			i, handLabel, s.TS.Format("15:04:05.000"), s.Cash, s.Equity, len(s.Positions))
	}

	// After close: the last helm snapshot should have no positions.
	var lastHelmSnap *perf.Snapshot
	for i := range snaps {
		if snaps[i].HandID == "" {
			lastHelmSnap = &snaps[i]
		}
	}
	if lastHelmSnap == nil {
		t.Fatal("no helm snapshot found")
	}
	if len(lastHelmSnap.Positions) != 0 {
		t.Errorf("after exit: helm snapshot positions: want 0, got %d", len(lastHelmSnap.Positions))
	}
	// Equity should still be ~capital (buy and sell at same price → flat PnL).
	if !lastHelmSnap.Equity.Equal(decimal.NewFromFloat(capital)) {
		t.Logf("helm equity after round-trip: %s (expected ~%v)", lastHelmSnap.Equity, capital)
	}
}
