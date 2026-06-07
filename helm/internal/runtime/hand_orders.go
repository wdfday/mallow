package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/infra/poslog"
	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/position"
)

// polledOrder pairs an open order with its freshly-fetched exchange state.
// result is nil when err != nil. Produced by fetchPendingOrders, consumed by applyPolledOrders.
type polledOrder struct {
	order  handdomain.Order
	result *exchange.OrderResult
	err    error
}

// pollBatch is the combined output of one off-loop REST poll: open-order states plus
// bracket-order states. Fetched in a goroutine (pure I/O), applied on the run loop.
type pollBatch struct {
	orders   []polledOrder
	brackets []bracketState
}

// fetchPendingOrders is the I/O phase of polling: GetOrder for every open order.
// Pure REST — it only reads a snapshot of h.orders and never mutates hand state — so it runs
// OFF the actor loop (in a goroutine) without racing the loop's own mutations. The run loop
// applies the result on-loop via applyPolledOrders. Keeping the GetOrder fan-out off-loop is
// what stops a slow poll from starving the fill mailbox (see runLoop / hand.fillSignal).
func (h *Hand) fetchPendingOrders(ctx context.Context) []polledOrder {
	h.mu.RLock()
	var pending []handdomain.Order
	for _, o := range h.orders {
		switch o.Status {
		case "new", "accepted", "pending_new", "partially_filled", "submitted":
			pending = append(pending, o)
		}
	}
	h.mu.RUnlock()

	out := make([]polledOrder, 0, len(pending))
	for _, o := range pending {
		result, err := h.helmRuntime.Exchange.GetOrder(ctx, h.helmRuntime.Creds, o.ID)
		out = append(out, polledOrder{order: o, result: result, err: err})
	}
	return out
}

// applyPolledOrders is the state-mutation phase: it runs ON the actor loop, so it shares the
// single-owner invariant with handleSignal/handleWsFill. The occasional REST it still issues
// (dust-remainder cancel, limit timeout) fires only for orders that actually changed — rare —
// so it does not meaningfully block the loop. Race-tolerant against the WS path via the
// existing seenFills / hasOrderFillPublished dedup guards.
func (h *Hand) applyPolledOrders(ctx context.Context, polled []polledOrder) {
	for _, p := range polled {
		o := p.order
		if p.err != nil {
			slog.Warn("hand: poll order failed", "order_id", o.ID, "err", p.err)
			continue
		}
		result := p.result
		h.mu.Lock()
		for i := range h.orders {
			if h.orders[i].ID == o.ID {
				h.orders[i].Status = result.Status
				h.orders[i].FilledQty = result.FilledQty
				h.orders[i].FilledAvg = result.FilledAvg
				break
			}
		}
		h.mu.Unlock()
		switch result.Status {
		case "filled":
			side := "buy"
			if result.Side == exchange.Sell {
				side = "sell"
			}
			h.mu.Lock()
			if _, seen := h.seenFills[result.ID]; seen {
				// WS full-fill or REST-immediate path already applied this order.
				h.mu.Unlock()
				break
			}
			h.mu.Unlock()
			if h.helmRuntime.hasOrderFillPublished(result.ID) {
				h.mu.Lock()
				delete(h.partialApplied, result.ID)
				h.mu.Unlock()
				break
			}
			// GetOrder returns cumulative FilledQty. Deduct any qty already applied
			// by the WS partial-fill path (registry → rt.ReportFill + MarkPartialApplied)
			// so we only apply the remaining delta — no double-count.
			h.mu.Lock()
			state := h.partialApplied[result.ID]

			cumQty, cumCommission := h.helmRuntime.normalizeCommission(result.Symbol, result.Side, result.FilledQty, result.FilledAvg, result.Commission, result.CommissionAsset)

			netQty := cumQty.Sub(state.Qty)
			netCommission := cumCommission.Sub(state.Commission)
			if netCommission.IsNegative() {
				netCommission = decimal.Zero
			}
			// Only consume pendingExits here for the netQty==0 path.
			// When netQty > 0, applyFill reads and deletes pendingExits itself
			// inside its own lock section — that is where scheduleBracketPlacement
			// is triggered. Deleting here first would leave applyFill with an empty
			// pendingExits lookup → hasExitBracket=false → no OCO bracket placed.
			var pendingExit exitPending
			var hasPendingExit bool
			if !netQty.IsPositive() {
				pendingExit, hasPendingExit = h.pendingExits[result.ID]
				if hasPendingExit {
					delete(h.pendingExits, result.ID)
				}
			}
			h.seenFills[result.ID] = time.Now()
			h.mu.Unlock()
			if netQty.IsPositive() {
				h.applyFill(ctx, result.ID, result.Symbol, side, netQty, result.FilledAvg, netCommission, "poll")
			} else {
				// netQty == 0: all partial fills were already applied by the WS path
				// (MarkPartialApplied). Fee fix (OKX base-asset fee deduction) causes
				// netQty to be exactly zero — previously it was fee_amount > 0 which
				// went through applyFill. We must still complete the poslog fill so the
				// leg transitions PhaseEntering → PhaseOpen, otherwise EXIT signals
				// see no PhaseOpen leg and return NO_POS.
				//
				// Pass cumQty (net, fee-deducted) so leg.Qty reflects actual held qty.
				h.mu.Lock()
				delete(h.partialApplied, result.ID)
				h.mu.Unlock()
				h.completeWsFillFromREST(ctx, result.ID, result.Symbol, side,
					cumQty, result.FilledAvg, cumCommission, pendingExit, false)
			}

		case "new", "accepted", "submitted", "pending_new":
			// Limit order timeout: cancel (and optionally re-place) if it hasn't filled
			// within LimitTimeoutSec seconds.
			if o.Type == "limit" && h.cfg.limitTimeoutSec > 0 {
				timeout := time.Duration(h.cfg.limitTimeoutSec) * time.Second
				if time.Since(o.SubmitTime) > timeout {
					h.handleLimitTimeout(ctx, o, result)
				}
			}

		case "partially_filled":
			// Limit timeout for partially-filled orders (same policy).
			if o.Type == "limit" && h.cfg.limitTimeoutSec > 0 {
				timeout := time.Duration(h.cfg.limitTimeoutSec) * time.Second
				if time.Since(o.SubmitTime) > timeout && result.FilledQty.IsPositive() {
					h.handleLimitTimeout(ctx, o, result)
					break
				}
			}
			// Cancel remainder when the unfilled portion is dust (< 2% of original qty).
			// This prevents tiny open orders that may never fill or fail min-notional.
			if result.FilledQty.IsPositive() && o.Qty.IsPositive() {
				remaining := o.Qty.Sub(result.FilledQty)
				if remaining.IsPositive() && remaining.Div(o.Qty).LessThan(decimal.NewFromFloat(0.02)) {
					if err := h.helmRuntime.Exchange.CancelOrder(ctx, h.helmRuntime.Creds, o.ID); err != nil {
						slog.Warn("hand: cancel partial remainder failed", "order_id", o.ID, "err", err)
					} else {
						slog.Info("hand: cancelled dust remainder on partial fill",
							"order_id", o.ID, "filled", result.FilledQty, "original", o.Qty)
						h.helmRuntime.EmitEvent(natsapi.HelmEvent{
							HandID:  h.id.String(),
							Code:    CodeOrderPartialCancel,
							Symbol:  result.Symbol,
							OrderID: o.ID,
							Qty:     result.FilledQty,
							Reason:  fmt.Sprintf("remainder %.4f < 2%% of original %.4f — cancelled", remaining.InexactFloat64(), o.Qty.InexactFloat64()),
							Msg:     "order: dust remainder cancelled",
						})
					}
					side := "buy"
					if result.Side == exchange.Sell {
						side = "sell"
					}
					h.mu.Lock()
					if _, seen := h.seenFills[result.ID]; !seen {
						// Same cumulative-deduct as the "filled" case: GetOrder returns
						// total filled qty; subtract what the WS partial path already applied.
						state := h.partialApplied[result.ID]

						cumQty := result.FilledQty
						cumCommission := result.Commission
						if result.Side == exchange.Buy && result.Commission.IsPositive() && result.CommissionAsset != "" &&
							strings.HasPrefix(result.Symbol, result.CommissionAsset) {
							cumQty = cumQty.Sub(result.Commission)
							if result.FilledAvg.IsPositive() {
								cumCommission = result.Commission.Mul(result.FilledAvg)
							}
						}

						netQty := cumQty.Sub(state.Qty)
						netCommission := cumCommission.Sub(state.Commission)
						if netCommission.IsNegative() {
							netCommission = decimal.Zero
						}
						h.seenFills[result.ID] = time.Now()
						// Consume pendingExits even when netQty==0: the WS partial path
						// already applied all qty but never had pendingExits populated
						// (set after PlaceOrder returned). Without this, no bracket is placed.
						pendingExit, hasPendingExit := h.pendingExits[result.ID]
						if hasPendingExit {
							delete(h.pendingExits, result.ID)
						}
						posIDForBracket := h.pendingOrderPos[result.ID]
						h.mu.Unlock()
						if netQty.IsPositive() {
							h.applyFill(ctx, result.ID, result.Symbol, side, netQty, result.FilledAvg, netCommission, "partial_cancel")
						} else {
							h.mu.Lock()
							delete(h.partialApplied, result.ID)
							h.mu.Unlock()
							if hasPendingExit {
								// WS already applied all qty; schedule the missing bracket now.
								h.completeWsFillFromREST(ctx, result.ID, result.Symbol, side,
									result.FilledQty, result.FilledAvg, decimal.Zero, pendingExit, false)
								_ = posIDForBracket // captured above, used by completeWsFillFromREST internally
							}
						}
					} else {
						h.mu.Unlock()
					}
				}
			}

		case "cancelled", "rejected", "expired":
			// Capture pre-cancel phase BEFORE publishAndApply changes leg state.
			// Used to emit the correct position-level cancel event below.
			h.mu.RLock()
			posID := h.pendingOrderPos[o.ID]
			preCancelPhase := h.pos.LegPhase(posID)
			h.mu.RUnlock()
			if posID != "" {
				payload, _ := json.Marshal(poslog.OrderCancelledPayload{
					OrderID: o.ID,
					Reason:  result.Status,
				})
				h.publishAndApply(ctx, poslog.Event{
					ID:         o.ID + "_cancelled",
					HandID:     h.id.String(),
					HelmID:     h.helmID.String(),
					PositionID: posID,
					Kind:       poslog.KindOrderCancelled,
					Payload:    payload,
					At:         time.Now().UTC(),
				})
			}
			// Terminal: remove from routing map so cancelled orders don't accumulate.
			h.helmRuntime.RemoveOrderTracking(o.ID)
			slog.Info("order: "+result.Status,
				"hand_id", h.id,
				"symbol", result.Symbol,
				"order_id", o.ID,
				"side", result.Side,
				"status", result.Status,
			)
			h.emitEvent(natsapi.HelmEvent{
				Code:    CodeOrderCancelled,
				Symbol:  result.Symbol,
				OrderID: o.ID,
				Side:    string(result.Side),
				Reason:  result.Status,
				Msg:     "order: " + result.Status,
			})
			// Position-level cancel events: tell users the trade intent was cancelled.
			switch preCancelPhase {
			case position.PhaseEntering:
				h.emitEvent(natsapi.HelmEvent{
					Code:    CodePositionEnterCancelled,
					Symbol:  result.Symbol,
					OrderID: o.ID,
					Side:    string(result.Side),
					Reason:  result.Status,
					Msg:     "position: entry cancelled — no position opened",
				})
			case position.PhaseAdding:
				h.emitEvent(natsapi.HelmEvent{
					Code:    CodePositionAddCancelled,
					Symbol:  result.Symbol,
					OrderID: o.ID,
					Side:    string(result.Side),
					Reason:  result.Status,
					Msg:     "position: pyramid add cancelled — position reverts to prior state",
				})
			}
		}
	}

	// Compact h.orders: prune terminal entries so the slice doesn't grow unboundedly.
	// Runs after all pending orders are processed — no information is lost.
	h.mu.Lock()
	n := 0
	var terminalKeys []string
	for _, o := range h.orders {
		switch o.Status {
		case "filled", "cancelled", "rejected", "expired":
			// terminal — drop, and collect routing keys to release after the lock.
			if o.ClientOrderID != "" {
				terminalKeys = append(terminalKeys, o.ClientOrderID)
			}
			terminalKeys = append(terminalKeys, o.ID)
		default:
			h.orders[n] = o
			n++
		}
	}
	h.orders = h.orders[:n]
	h.mu.Unlock()
	// Release any lingering clid/exchange-id routing entries for terminal orders.
	// WS full-fills already clean these; this catches poll/REST-immediate-only fills
	// so the routing map never accumulates stale entries.
	for _, k := range terminalKeys {
		h.helmRuntime.RemoveOrderTracking(k)
	}
}

// handleLimitTimeout cancels a stale limit order and, depending on LimitFallback,
// either records a cancel-only event or re-places the remaining qty as a market order.
func (h *Hand) handleLimitTimeout(ctx context.Context, o handdomain.Order, polled *exchange.OrderResult) {
	age := time.Since(o.SubmitTime).Truncate(time.Second)

	// Snapshot poslog context before cancelling so we can propagate it to the fallback.
	// Snapshot origPosID before cancelling. origPhase will be re-read AFTER any
	// partial applyFill so it reflects the post-fill leg state — avoids targeting
	// a dead (PhaseIdle) position with KindOrderCancelled and the fallback order.
	h.mu.RLock()
	origPosID := h.pendingOrderPos[o.ID]
	h.mu.RUnlock()

	if cancelErr := h.helmRuntime.Exchange.CancelOrder(ctx, h.helmRuntime.Creds, o.ID); cancelErr != nil {
		slog.Warn("hand: limit timeout cancel failed", "order_id", o.ID, "err", cancelErr)
		return
	}

	cumFilledQty := polled.FilledQty
	cumCommission := polled.Commission
	if polled.Side == exchange.Buy && polled.Commission.IsPositive() && polled.CommissionAsset != "" &&
		strings.HasPrefix(polled.Symbol, polled.CommissionAsset) {
		cumFilledQty = cumFilledQty.Sub(polled.Commission)
		if polled.FilledAvg.IsPositive() {
			cumCommission = polled.Commission.Mul(polled.FilledAvg)
		}
	}
	if cumFilledQty.IsPositive() {
		// Apply the delta that hasn't been applied yet. polled.FilledQty is cumulative
		// (from GetOrder REST); WS partials may have already applied some of it via
		// registry → rt.ReportFill + MarkPartialApplied.
		side := "buy"
		if polled.Side == exchange.Sell {
			side = "sell"
		}
		h.mu.Lock()
		if _, seen := h.seenFills[o.ID]; !seen {
			state := h.partialApplied[o.ID]
			netQty := cumFilledQty.Sub(state.Qty)
			netCommission := cumCommission.Sub(state.Commission)
			if netCommission.IsNegative() {
				netCommission = decimal.Zero
			}
			h.seenFills[o.ID] = time.Now()
			h.mu.Unlock()
			if netQty.IsPositive() {
				h.applyFill(ctx, o.ID, polled.Symbol, side, netQty, polled.FilledAvg, netCommission, "limit_timeout_partial")
			} else {
				h.mu.Lock()
				delete(h.partialApplied, o.ID)
				h.mu.Unlock()
			}
		} else {
			h.mu.Unlock()
		}
	}
	// alreadyFilledQty is used below to compute the remaining qty for the fallback order.
	alreadyFilledQty := cumFilledQty

	// Re-snapshot origPhase AFTER partial applyFill: if the partial fully closed the
	// position (e.g. fill qty == leg qty), the leg is now PhaseIdle. Publishing
	// KindOrderCancelled or placing a fallback market order on a dead position would error.
	h.mu.RLock()
	origPhase := h.pos.LegPhase(origPosID)
	h.mu.RUnlock()
	if origPhase == position.PhaseIdle {
		slog.Info("hand: limit timeout partial fill fully closed position — skipping cancel event and fallback",
			"order_id", o.ID, "filled", alreadyFilledQty)
		return
	}

	// Publish KindOrderCancelled for the original limit so:
	// 1. pendingOrderPos[o.ID] is cleared (pollOrders won't re-publish on next cycle)
	// 2. The leg phase transitions back (Entering→Idle, Exiting→Open) so the fallback
	//    order_placed applies cleanly.
	if origPosID != "" {
		cancelPayload, _ := json.Marshal(poslog.OrderCancelledPayload{
			OrderID: o.ID,
			Reason:  "limit_timeout",
		})
		h.publishAndApply(ctx, poslog.Event{
			ID:         o.ID + "_cancelled",
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: origPosID,
			Kind:       poslog.KindOrderCancelled,
			Payload:    cancelPayload,
			At:         time.Now().UTC(),
		})
	}

	remainingQty := o.Qty.Sub(alreadyFilledQty)
	slog.Info("hand: limit order timed out", "order_id", o.ID, "age", age,
		"filled", alreadyFilledQty, "remaining", remainingQty, "fallback", h.cfg.limitFallback)

	h.helmRuntime.EmitEvent(natsapi.HelmEvent{
		HandID:  h.id.String(),
		Code:    CodeOrderLimitTimeout,
		Symbol:  o.Symbol,
		OrderID: o.ID,
		Qty:     remainingQty,
		Reason:  fmt.Sprintf("limit unfilled after %s (filled %s / %s)", age, alreadyFilledQty, o.Qty),
		Msg:     "order: limit timeout",
	})

	if h.cfg.limitFallback == handdomain.LimitFallbackMarket && remainingQty.IsPositive() {
		result, err := h.helmRuntime.Exchange.PlaceOrder(ctx, h.helmRuntime.Creds, exchange.OrderRequest{
			Symbol: o.Symbol,
			Side:   exchange.OrderSide(o.Side),
			Type:   exchange.Market,
			Qty:    remainingQty,
		})
		if err != nil {
			slog.Error("hand: limit fallback market order failed", "order_id", o.ID, "err", err)
			return
		}
		slog.Info("hand: limit fallback market placed", "new_order_id", result.ID, "qty", remainingQty)
		h.helmRuntime.EmitEvent(natsapi.HelmEvent{
			HandID:  h.id.String(),
			Code:    CodeOrderLimitFallback,
			Symbol:  o.Symbol,
			OrderID: result.ID,
			Side:    string(o.Side),
			Qty:     remainingQty,
			Reason:  fmt.Sprintf("fallback from timed-out limit %s", o.ID),
			Msg:     "order: limit fallback to market",
		})
		h.trackOrder(result.ID)

		// Register fallback order in poslog using the same positionID as the original
		// limit. origPhase tells us whether this is an entry or exit fallback:
		//   Idle/Entering → entry (original was entering; cancel reset to Idle)
		//   Open/Exiting  → exit  (original was exiting; cancel reset to Open)
		if origPosID != "" {
			isExitFallback := origPhase == position.PhaseExiting
			fallbackPayload, _ := json.Marshal(poslog.OrderPlacedPayload{
				OrderID:   result.ID,
				Symbol:    o.Symbol,
				Side:      string(o.Side),
				Qty:       remainingQty.String(),
				Price:     "0",
				OrderType: "market",
				IsClose:   isExitFallback,
			})
			h.publishAndApply(ctx, poslog.Event{
				ID:         result.ID,
				HandID:     h.id.String(),
				HelmID:     h.helmID.String(),
				PositionID: origPosID,
				Kind:       poslog.KindOrderPlaced,
				Payload:    fallbackPayload,
				At:         time.Now().UTC(),
			})
		}

		// Add to h.orders so pollOrders can detect a delayed fill.
		now := time.Now().UTC()
		h.mu.Lock()
		h.orders = append(h.orders, handdomain.Order{
			HandId:     h.id.String(),
			HelmId:     h.helmID.String(),
			ID:         result.ID,
			Symbol:     o.Symbol,
			Side:       string(o.Side),
			Qty:        remainingQty,
			Type:       "market",
			Status:     result.Status,
			FilledQty:  result.FilledQty,
			FilledAvg:  result.FilledAvg,
			SubmitTime: now,
		})
		h.mu.Unlock()

		if result.Status == "filled" {
			h.mu.Lock()
			h.seenFills[result.ID] = time.Now()
			h.mu.Unlock()
			h.applyFill(ctx, result.ID, o.Symbol, string(o.Side), result.FilledQty, result.FilledAvg, decimal.Zero, "limit_fallback")
		}
	}
}

// recoverAmbiguousPlace handles a PlaceOrder failure that does NOT clearly mean the
// order was rejected — a timeout or network drop, where the order may or may not have
// reached the exchange. Because we generated and tracked the clid before placing, we can
// ask the exchange whether an order with that clid exists. If it does, the placement
// actually succeeded and we return its result so the caller treats it as placed (keeping
// the clid tracked) rather than untracking and re-entering. Returns nil when the error is
// a clear reject, the exchange can't query by clid, or no such order exists.
func (h *Hand) recoverAmbiguousPlace(ctx context.Context, symbol, clid string, placeErr error) *exchange.OrderResult {
	if !exchange.IsAmbiguousPlaceError(placeErr) {
		return nil
	}
	q, ok := h.helmRuntime.Exchange.(exchange.ClientOrderQuerier)
	if !ok {
		return nil
	}
	market := exchange.MarketSpot
	if h.helmRuntime.Creds.AccountType == exchange.AccountFuturesUSDM ||
		h.helmRuntime.Creds.AccountType == exchange.AccountFuturesCOINM {
		market = exchange.MarketFutures
	}

	// Poll a few times with backoff: after a timeout the order may still be propagating
	// at the exchange, so an immediate lookup can falsely report "not found". A nil result
	// is "not confirmed yet" — keep trying; a non-nil result is authoritative.
	delays := []time.Duration{300 * time.Millisecond, 700 * time.Millisecond, 1500 * time.Millisecond}
	for attempt, d := range delays {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(d):
		}
		qCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		res, err := q.GetOrderByClientOrderID(qCtx, h.helmRuntime.Creds, symbol, market, clid)
		cancel()
		if err != nil || res == nil {
			continue
		}
		slog.Warn("order: ambiguous placement recovered via clid — order exists on exchange",
			"hand_id", h.id, "symbol", symbol, "client_order_id", clid,
			"order_id", res.ID, "status", res.Status, "attempt", attempt+1, "place_err", placeErr,
		)
		return res
	}
	return nil
}

// isLotSizeError returns true when the error is a persistent sizing constraint —
// lot size, min notional, or filter validation — that will recur on every entry
// at the current configured quantity, regardless of market conditions.
func isLotSizeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{
		"lot_size", "min_notional", "notional", "price_filter",
		"lot size", "min notional", "minimum quantity", "minimum amount",
		"filter failure", "below minimum", "invalid quantity",
		"order size", "qty too small", "quantity too small",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// spotBaseAsset extracts the base asset from a spot symbol by stripping common
// quote assets (USDT, BTC, ETH, BNB, USDC, BUSD, TUSD, FDUSD).
// "ETHUSDT" → "ETH", "BTCUSDT" → "BTC", "ETHBTC" → "ETH".
// Returns the full symbol unchanged if no known quote asset suffix is found.
func spotBaseAsset(symbol string) string {
	for _, quote := range []string{"USDT", "USDC", "BUSD", "TUSD", "FDUSD", "BTC", "ETH", "BNB"} {
		if strings.HasSuffix(symbol, quote) && len(symbol) > len(quote) {
			return symbol[:len(symbol)-len(quote)]
		}
	}
	return symbol
}
