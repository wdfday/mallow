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
	"mallow/helm/internal/infra/poslog"
	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/position"
)

func (h *Hand) pollOrders(ctx context.Context) {
	h.mu.RLock()
	var pending []handdomain.Order
	for _, o := range h.orders {
		switch o.Status {
		case "new", "accepted", "pending_new", "partially_filled", "submitted":
			pending = append(pending, o)
		}
	}
	h.mu.RUnlock()

	for _, o := range pending {
		result, err := h.rt.Exchange.GetOrder(ctx, h.rt.Creds, o.ID)
		if err != nil {
			slog.Warn("bot: poll order failed", "order_id", o.ID, "err", err)
			continue
		}
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
				// WS or REST-immediate path already applied this fill.
				h.mu.Unlock()
				break
			}
			h.seenFills[result.ID] = struct{}{}
			h.mu.Unlock()
			h.applyFill(ctx, result.ID, result.Symbol, side, result.FilledQty, result.FilledAvg, "poll")

		case "new", "accepted", "submitted", "pending_new":
			// Limit order timeout: cancel (and optionally re-place) if it hasn't filled
			// within LimitTimeoutSec seconds.
			if o.Type == "limit" && h.limitTimeoutSec > 0 {
				timeout := time.Duration(h.limitTimeoutSec) * time.Second
				if time.Since(o.SubmitTime) > timeout {
					h.handleLimitTimeout(ctx, o, result)
				}
			}

		case "partially_filled":
			// Limit timeout for partially-filled orders (same policy).
			if o.Type == "limit" && h.limitTimeoutSec > 0 {
				timeout := time.Duration(h.limitTimeoutSec) * time.Second
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
					if err := h.rt.Exchange.CancelOrder(ctx, h.rt.Creds, o.ID); err != nil {
						slog.Warn("bot: cancel partial remainder failed", "order_id", o.ID, "err", err)
					} else {
						slog.Info("bot: cancelled dust remainder on partial fill",
							"order_id", o.ID, "filled", result.FilledQty, "original", o.Qty)
						h.recordActivity(ActivityEntry{
							At:      time.Now(),
							Code:    CodeOrderPartialCancel,
							Symbol:  result.Symbol,
							OrderID: o.ID,
							Qty:     result.FilledQty,
							Reason:  fmt.Sprintf("remainder %.4f < 2%% of original %.4f — cancelled", remaining.InexactFloat64(), o.Qty.InexactFloat64()),
						})
					}
					side := "buy"
					if result.Side == exchange.Sell {
						side = "sell"
					}
					h.mu.Lock()
					if _, seen := h.seenFills[result.ID]; !seen {
						h.seenFills[result.ID] = struct{}{}
						h.mu.Unlock()
						h.applyFill(ctx, result.ID, result.Symbol, side, result.FilledQty, result.FilledAvg, "partial_cancel")
					} else {
						h.mu.Unlock()
					}
				}
			}

		case "cancelled", "rejected", "expired":
			h.mu.RLock()
			posID := h.pendingOrderPos[o.ID]
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
		}
	}
}

// handleLimitTimeout cancels a stale limit order and, depending on LimitFallback,
// either records a cancel-only event or re-places the remaining qty as a market order.
func (h *Hand) handleLimitTimeout(ctx context.Context, o handdomain.Order, polled *exchange.OrderResult) {
	age := time.Since(o.SubmitTime).Truncate(time.Second)

	// Snapshot poslog context before cancelling so we can propagate it to the fallback.
	h.mu.RLock()
	origPosID := h.pendingOrderPos[o.ID]
	origPhase := h.pos.LegPhase(origPosID)
	h.mu.RUnlock()

	if cancelErr := h.rt.Exchange.CancelOrder(ctx, h.rt.Creds, o.ID); cancelErr != nil {
		slog.Warn("bot: limit timeout cancel failed", "order_id", o.ID, "err", cancelErr)
		return
	}

	alreadyFilledQty := polled.FilledQty
	if alreadyFilledQty.IsPositive() {
		// Apply partial fill before re-placing remainder.
		side := "buy"
		if polled.Side == exchange.Sell {
			side = "sell"
		}
		h.mu.Lock()
		if _, seen := h.seenFills[o.ID]; !seen {
			h.seenFills[o.ID] = struct{}{}
			h.mu.Unlock()
			h.applyFill(ctx, o.ID, polled.Symbol, side, alreadyFilledQty, polled.FilledAvg, "limit_timeout_partial")
		} else {
			h.mu.Unlock()
		}
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
	slog.Info("bot: limit order timed out", "order_id", o.ID, "age", age,
		"filled", alreadyFilledQty, "remaining", remainingQty, "fallback", h.limitFallback)

	h.recordActivity(ActivityEntry{
		At:      time.Now(),
		Code:    CodeOrderLimitTimeout,
		Symbol:  o.Symbol,
		OrderID: o.ID,
		Qty:     remainingQty,
		Reason:  fmt.Sprintf("limit unfilled after %s (filled %s / %s)", age, alreadyFilledQty, o.Qty),
	})

	if h.limitFallback == handdomain.LimitFallbackMarket && remainingQty.IsPositive() {
		result, err := h.rt.Exchange.PlaceOrder(ctx, h.rt.Creds, exchange.OrderRequest{
			Symbol: o.Symbol,
			Side:   exchange.OrderSide(o.Side),
			Type:   exchange.Market,
			Qty:    remainingQty,
		})
		if err != nil {
			slog.Error("bot: limit fallback market order failed", "order_id", o.ID, "err", err)
			return
		}
		slog.Info("bot: limit fallback market placed", "new_order_id", result.ID, "qty", remainingQty)
		h.recordActivity(ActivityEntry{
			At:      time.Now(),
			Code:    CodeOrderLimitFallback,
			Symbol:  o.Symbol,
			OrderID: result.ID,
			Side:    string(o.Side),
			Qty:     remainingQty,
			Reason:  fmt.Sprintf("fallback from timed-out limit %s", o.ID),
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
			h.seenFills[result.ID] = struct{}{}
			h.mu.Unlock()
			h.applyFill(ctx, result.ID, o.Symbol, string(o.Side), result.FilledQty, result.FilledAvg, "limit_fallback")
		}
	}
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
