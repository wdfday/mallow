package actor

// checkExits internal test — verifies the local exit monitor tags the
// delivered Signal with the correct strategy.ExitKind (sl/tp) instead of
// leaving it untagged. checkExits is unexported and only reachable via the
// run loop's ticker in production, so this lives in package actor (not
// actor_test) to call it directly; poslog event helpers mirror
// reconcile_test.go's recPlaced/recFilled (kept self-contained per that
// file's own precedent for duplicating across test files in this package).

import (
	"encoding/json"
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
	rt.UpdatePrice(symbol, decimal.NewFromFloat(49_000)) // below the SL level set below

	strat := strategy.NewSignalFollower(0.3)
	tact := tactics.New(tactics.DefaultSizingConfig())
	h := NewHand(uuid.New(), rt.HelmID, rt, strat, tact, false, 1, 10*time.Second,
		nil, domain.OrderTypeMarket, 0, "", domain.HandGuardConfig{}, decimal.Zero)
	h.Symbol = symbol

	// Seed one active leg via poslog replay (same mechanism as a real restart) —
	// checkExits requires h.pos.ActiveCount() > 0 or it treats the exit level as
	// stale (external close) instead of firing.
	positionID := "seed-1"
	placed := poslog.OrderPlacedPayload{OrderID: positionID, Symbol: symbol, Side: "buy", Qty: "0.01", Price: "50000", OrderType: "market"}
	placedPayload, _ := json.Marshal(placed)
	if err := h.pos.Apply(poslog.Event{ID: positionID, PositionID: positionID, Kind: poslog.KindOrderPlaced, Payload: placedPayload, At: time.Now()}); err != nil {
		t.Fatalf("seed KindOrderPlaced: %v", err)
	}
	filled := poslog.OrderFilledPayload{OrderID: positionID, FillPrice: "50000", FillQty: "0.01", Source: "ws"}
	filledPayload, _ := json.Marshal(filled)
	if err := h.pos.Apply(poslog.Event{ID: positionID + "_filled", PositionID: positionID, Kind: poslog.KindOrderFilled, Payload: filledPayload, At: time.Now()}); err != nil {
		t.Fatalf("seed KindOrderFilled: %v", err)
	}

	h.exitLevels[symbol] = exitLevel{
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
