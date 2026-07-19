package actor_test

// Pyramiding avg-anchor add gate: an add is allowed only when the existing leg is winning
// (current price beyond the blended avg entry). Losing/flat re-signals are blocked, so the
// hand presses winners and never averages down — even if the strategy re-signals every bar.
// See docs/pyramiding-design.md (avg-anchor decision).
//
// go test ./internal/runtime/ -run TestPyramid -count=1

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/core/tactics"
	"mallow/helm/internal/module/hand/domain"
)

// addPyramidHand mirrors addSimHand but with pyramid (merge) mode enabled.
func addPyramidHand(rt *actor.HelmRuntime, symbol string, qty float64, maxUnits int) *actor.Hand {
	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.SizingConfig{
		Mode:     tactics.SizingFixedQty,
		FixedQty: decimal.NewFromFloat(qty),
	})
	h := actor.NewHand(
		uuid.New(), rt.HelmID, rt,
		strat, tact,
		true, maxUnits, 0, // pyramid=true
		nil, domain.OrderTypeMarket, 0, domain.LimitFallbackCancel,
		domain.HandGuardConfig{}, decimal.Zero,
	)
	h.Symbol = symbol
	h.StrategyName = "signal_follower"
	h.EnableEventSink()
	rt.AddHand(h, &domain.Hand{ID: h.ID(), HelmID: rt.HelmID, Symbols: domain.StringSlice{h.Symbol}})
	return h
}

func TestPyramid_AvgGate_AddsOnWinnerBlocksOnLoser(t *testing.T) {
	const symbol = "BTCUSDT"
	sim := newSim(100) // every fill at 100 → blended avg stays 100
	rt := buildSimRuntime(sim, 1_000_000, 10)
	defer rt.Stop()

	h := addPyramidHand(rt, symbol, 0.001, 5)
	rt.UpdatePrice(symbol, decimal.NewFromFloat(100))
	h.Start()
	defer h.Stop()

	// 1) First entry @ 100 → opens the leg (avg = 100).
	ch := h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCodeCh(t, ch, actor.CodeOrderFilled, simWait)
	if sim.placedCount() != 1 {
		t.Fatalf("want 1 order after first entry, got %d", sim.placedCount())
	}

	// 2) Price rises to 105 (> avg 100) → winning → add allowed.
	rt.UpdatePrice(symbol, decimal.NewFromFloat(105))
	ch = h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCodeCh(t, ch, actor.CodeOrderFilled, simWait)
	if sim.placedCount() != 2 {
		t.Fatalf("want 2 orders after winning add, got %d", sim.placedCount())
	}

	// 3) Price drops to 95 (< avg 100) → not winning → add BLOCKED by the gate.
	rt.UpdatePrice(symbol, decimal.NewFromFloat(95))
	ch = h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCodeCh(t, ch, actor.CodeSignalDoNothing, simWait) // pyramid gate fires
	noCode(t, h, actor.CodeOrderPlaced, simWait)
	if sim.placedCount() != 2 {
		t.Errorf("pyramid gate must block a losing add; want 2 orders, got %d", sim.placedCount())
	}

	// 4) Price recovers above avg → add allowed again.
	rt.UpdatePrice(symbol, decimal.NewFromFloat(101))
	ch = h.Subscribe(64)
	h.DeliverSignal(longSignalFor(symbol))
	mustWaitCodeCh(t, ch, actor.CodeOrderFilled, simWait)
	if sim.placedCount() != 3 {
		t.Errorf("want 3 orders after price recovers above avg, got %d", sim.placedCount())
	}
}
