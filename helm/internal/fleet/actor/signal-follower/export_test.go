package signalfollower

// Test-only exported hooks onto Hand's unexported internals. Needed because
// some white-box tests (hand_exits_internal_test.go, helm_actor_test.go) also
// require a concrete *actor.HelmRuntime — importing package actor from here
// would create an import cycle (actor imports signalfollower for Hand), so
// those tests live in package signalfollower_test instead and reach Hand's
// private state only through this file. Standard Go export_test.go idiom:
// this file is compiled only for `go test`, never into the production binary.

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/journal/poslog"
)

// TestSeedPoslogEvent applies a poslog event directly to the hand's in-memory
// position state — test setup only, bypassing the normal signal→fill flow.
func (h *Hand) TestSeedPoslogEvent(e poslog.Event) error {
	return h.pos.Apply(e)
}

// TestSetExitLevel seeds an exit level for tradeID, mirroring what the run
// loop sets after resolving SL/TP from a fill.
func (h *Hand) TestSetExitLevel(tradeID, symbol, side string, stopLoss, takeProfit decimal.Decimal, exchangeOrderIDs []string, groupID string) {
	h.mu.Lock()
	h.exitLevels[tradeID] = exitLevel{
		Symbol:           symbol,
		Side:             side,
		StopLoss:         stopLoss,
		TakeProfit:       takeProfit,
		ExchangeOrderIDs: exchangeOrderIDs,
		GroupID:          groupID,
		PlacedAt:         time.Now(),
	}
	h.mu.Unlock()
}

// TestExitLevel reads back an exit level for assertions.
func (h *Hand) TestExitLevel(tradeID string) (stopLoss decimal.Decimal, exists bool) {
	h.mu.RLock()
	el, ok := h.exitLevels[tradeID]
	h.mu.RUnlock()
	return el.StopLoss, ok
}

// TestCheckExits runs the unexported checkExits run-loop step, normally
// reachable only via the run loop's ticker in production.
func (h *Hand) TestCheckExits() { h.checkExits() }

// TestCancelExitOrders runs the unexported cancelExitOrders step under the
// same lock discipline the run loop uses.
func (h *Hand) TestCancelExitOrders(ctx context.Context, tradeID, symbol, marketKind string) {
	h.mu.Lock()
	h.cancelExitOrders(ctx, tradeID, symbol, marketKind)
	h.mu.Unlock()
}
