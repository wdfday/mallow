package actor

import (
	"context"
	"encoding/json"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/position"
	"mallow/helm/internal/fleet/perf"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/safe"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// publishAndApply publishes a poslog event to JetStream and applies it to the in-memory
// position state. The NATS publish failure is logged but does not halt the hand —
// the reconciler will detect and patch the missing event on next startup.
// Must NOT be called while b.mu is held.
func (h *Hand) publishAndApply(ctx context.Context, e poslog.Event) {
	if h.helmRuntime.PosLog != nil {
		if err := h.helmRuntime.PosLog.Publish(ctx, e); err != nil {
			h.log.Error("poslog publish failed", "event_id", e.ID, "kind", e.Kind, "err", err)
		}
	}
	h.mu.Lock()
	if err := h.pos.Apply(e); err != nil {
		h.log.Warn("poslog in-memory apply failed", "event_id", e.ID, "err", err)
	}
	// Keep pendingOrderPos in sync with poslog events.
	switch e.Kind {
	case poslog.KindOrderPlace:
		var p poslog.OrderPlacePayload
		if jsonErr := unmarshalJSON(e.Payload, &p); jsonErr == nil {
			h.pendingOrderPos[p.ClientOrderID] = e.PositionID
		}
	case poslog.KindOrderPlaced:
		var p poslog.OrderPlacedPayload
		if jsonErr := unmarshalJSON(e.Payload, &p); jsonErr == nil {
			h.pendingOrderPos[p.OrderID] = e.PositionID
			if p.ClientOrderID != "" {
				h.pendingOrderPos[p.ClientOrderID] = e.PositionID
			}
		}
	case poslog.KindOrderFilled:
		var p poslog.OrderFilledPayload
		if jsonErr := unmarshalJSON(e.Payload, &p); jsonErr == nil {
			posID := h.pendingOrderPos[p.OrderID]
			if posID != "" {
				for k, v := range h.pendingOrderPos {
					if v == posID {
						delete(h.pendingOrderPos, k)
					}
				}
			} else {
				delete(h.pendingOrderPos, p.OrderID)
			}
		}
	case poslog.KindOrderCancelled:
		var p poslog.OrderCancelledPayload
		if jsonErr := unmarshalJSON(e.Payload, &p); jsonErr == nil {
			posID := h.pendingOrderPos[p.OrderID]
			if posID != "" {
				for k, v := range h.pendingOrderPos {
					if v == posID {
						delete(h.pendingOrderPos, k)
					}
				}
			} else {
				delete(h.pendingOrderPos, p.OrderID)
			}
		}
	}
	h.mu.Unlock()
}

// publishOrderPlace emits KindOrderPlace (pre-flight intent) to the durable poslog.
func (h *Hand) publishOrderPlace(
	ctx context.Context,
	clientOrderID, symbol string,
	reply helmdomain.TradeReply,
	limitPrice decimal.Decimal,
	orderType exchange.OrderType,
	isExitIntent bool,
) {
	// Determine pyramid add vs new leg vs close.
	h.mu.RLock()
	isFlat := h.pos.IsFlat()
	primaryPosID := ""
	if leg := h.pos.PrimaryLeg(); leg != nil {
		primaryPosID = leg.PositionID
	}
	h.mu.RUnlock()

	isClose := isExitIntent
	isPyramidAdd := !isClose && h.cfg.pyramid && !isFlat

	var positionID string
	switch {
	case isClose || isPyramidAdd:
		positionID = primaryPosID
	default:
		positionID = clientOrderID // new leg: PositionID = opening client_order_id
	}
	if positionID == "" {
		return // no active leg context — skip
	}

	priceStr := "0"
	if limitPrice.IsPositive() {
		priceStr = limitPrice.String()
	}
	orderTypeStr := "market"
	if orderType == exchange.Limit {
		orderTypeStr = "limit"
	}

	payload, _ := json.Marshal(poslog.OrderPlacePayload{
		ClientOrderID: clientOrderID,
		Symbol:        symbol,
		Side:          reply.Side,
		Qty:           reply.Qty.String(),
		Price:         priceStr,
		OrderType:     orderTypeStr,
		StopLoss:      reply.StopLoss.String(),
		TakeProfit:    reply.TakeProfit.String(),
		IsPyramidAdd:  isPyramidAdd,
		IsClose:       isClose,
	})
	h.publishAndApply(ctx, poslog.Event{
		ID:         clientOrderID,
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindOrderPlace,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
}

// publishOrderCancelled emits KindOrderCancelled to the durable poslog.
func (h *Hand) publishOrderCancelled(
	ctx context.Context,
	orderID, positionID, reason string,
) error {
	payload, _ := json.Marshal(poslog.OrderCancelledPayload{
		OrderID: orderID,
		Reason:  reason,
	})
	h.publishAndApply(ctx, poslog.Event{
		ID:         orderID + "_cancelled",
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindOrderCancelled,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
	return nil
}

// publishOrderPlaced emits KindOrderPlaced to the durable poslog.
// isExitIntent: true when closing a position (not opening or adding).
func (h *Hand) publishOrderPlaced(
	ctx context.Context,
	orderID, clientOrderID, symbol string,
	reply helmdomain.TradeReply,
	limitPrice decimal.Decimal,
	orderType exchange.OrderType,
	isExitIntent bool,
) {
	// Determine pyramid add vs new leg vs close.
	h.mu.RLock()
	isFlat := h.pos.IsFlat()
	primaryPosID := ""
	if leg := h.pos.PrimaryLeg(); leg != nil {
		primaryPosID = leg.PositionID
	}
	h.mu.RUnlock()

	isClose := isExitIntent
	isPyramidAdd := !isClose && h.cfg.pyramid && !isFlat

	var positionID string
	switch {
	case isClose || isPyramidAdd:
		positionID = primaryPosID
	default:
		if clientOrderID != "" {
			positionID = clientOrderID
		} else {
			positionID = orderID
		}
	}
	if positionID == "" {
		return // no active leg context — skip
	}

	priceStr := "0"
	if limitPrice.IsPositive() {
		priceStr = limitPrice.String()
	}
	orderTypeStr := "market"
	if orderType == exchange.Limit {
		orderTypeStr = "limit"
	}

	payload, _ := json.Marshal(poslog.OrderPlacedPayload{
		OrderID:       orderID,
		ClientOrderID: clientOrderID,
		Symbol:        symbol,
		Side:          reply.Side,
		Qty:           reply.Qty.String(),
		Price:         priceStr,
		OrderType:     orderTypeStr,
		StopLoss:      reply.StopLoss.String(),
		TakeProfit:    reply.TakeProfit.String(),
		IsPyramidAdd:  isPyramidAdd,
		IsClose:       isClose,
	})
	h.publishAndApply(ctx, poslog.Event{
		ID:         orderID,
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindOrderPlaced,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
}

// publishOrderFilled emits KindOrderFilled to the durable poslog.
// pnl is the realized PnL for closing fills (zero for entry fills).
// commission is the fee paid for this fill (may be zero when exchange doesn't report it).
// deployedCapital is the quote cost of this specific fill (net_qty×price + entry_fee_quote);
// zero for exit fills (not a capital deployment). closeSource identifies what triggered the close.
func (h *Hand) publishOrderFilled(ctx context.Context, orderID string, qty, legQty, price, pnl, commission, deployedCapital decimal.Decimal, source, closeSource string) {
	h.mu.RLock()
	positionID := h.pendingOrderPos[orderID]
	var isBracketExit bool
	var snap position.LegSnapshot
	var hasSnap bool
	if positionID == "" {
		// Look for orderID in exitLevels
		for _, lv := range h.exitLevels {
			if slices.Contains(lv.ExchangeOrderIDs, orderID) {
				isBracketExit = true
				if primaryLeg := h.pos.PrimaryLeg(); primaryLeg != nil {
					positionID = primaryLeg.PositionID
					snap, hasSnap = h.pos.LegSnapshot(positionID)
				}
			}
			if positionID != "" {
				break
			}
		}
	} else {
		snap, hasSnap = h.pos.LegSnapshot(positionID)
	}
	isClosingFill := isBracketExit || (positionID != "" && h.pos.LegPhase(positionID) == position.PhaseExiting)
	h.mu.RUnlock()

	if positionID == "" {
		return // order not tracked in poslog
	}

	// Bracket (OCO) exits skip KindOrderFilled — the bracket order is the terminal
	// exit event; KindPositionClosed alone is sufficient for the poslog state machine.
	// Signal/kill/poll exits emit KindOrderFilled first to track fill progression.
	if !isBracketExit {
		payload, _ := json.Marshal(poslog.OrderFilledPayload{
			OrderID:         orderID,
			FillPrice:       price.String(),
			FillQty:         qty.String(),
			Source:          source,
			DeployedCapital: decimalToString(deployedCapital),
		})
		h.publishAndApply(ctx, poslog.Event{
			ID:         orderID + "_filled",
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: positionID,
			Kind:       poslog.KindOrderFilled,
			Payload:    payload,
			At:         time.Now().UTC(),
		})
	}

	if isClosingFill {
		now := time.Now().UTC()
		cp := poslog.PositionClosedPayload{
			OrderID:      positionID,
			ClosePrice:   price.String(),
			RealizedPnL:  pnl.String(),
			ExitReason:   poslog.ExitReason(closeSource),
			EntryOrderID: positionID,
			ExitOrderID:  orderID,
			Commission:   decimalToString(commission),
		}
		if hasSnap {
			cp.Symbol = snap.Symbol
			cp.Side = snap.Side
			effQty := legQty
			if effQty.IsZero() {
				effQty = qty
			}
			cp.Qty = effQty.String()
			cp.EntryPrice = snap.EntryPrice.String()
			cp.EntryAt = snap.OpenedAt
			if snap.StopLoss.IsPositive() {
				cp.StopLossPrice = snap.StopLoss.String()
			}
			if snap.TakeProfit.IsPositive() {
				cp.TakeProfitPrice = snap.TakeProfit.String()
			}
			cp.NEntries = snap.NEntries
			cp.DeployedCapital = decimalToString(snap.DeployedCapital)
		}
		closedPayload, _ := json.Marshal(cp)
		h.publishAndApply(ctx, poslog.Event{
			ID:         positionID + "_closed_" + orderID,
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: positionID,
			Kind:       poslog.KindPositionClosed,
			Payload:    closedPayload,
			At:         now,
		})
		h.appendTradeRecord(ctx, cp, commission, now)
	}
}

// appendTradeRecord publishes a completed round-trip trade to the HELM_TRADES
// JetStream stream. A TradePersister drains the stream into PostgreSQL.
//
// JetStream acts as a durable buffer: if the persister or PG is down briefly,
// trades queue up in NATS instead of being lost (which the previous direct-PG
// goroutine path could not guarantee).
//
// No-ops when TradeLog is not configured.
func (h *Hand) appendTradeRecord(ctx context.Context, cp poslog.PositionClosedPayload, exitCommission decimal.Decimal, exitAt time.Time) {
	tl := h.helmRuntime.TradeLog
	if tl == nil {
		return
	}
	// Compute PnL with a clean two-layer model:
	//
	//   gross_pnl  = (exit_qty × exit_price) − (net_qty × entry_price)
	//                Pure price movement; no fees on either side.
	//
	//   commission = entry_commission + exit_commission
	//                entry_commission is derived from deployed_capital:
	//                  deployed_capital = net_qty × entry_price + entry_commission
	//                  → entry_commission = deployed_capital − net_qty × entry_price
	//
	//   net_pnl    = gross_pnl − commission          ← computed by PG GENERATED column
	//
	// deployed_capital accumulates all entry fills (incl. pyramid adds) with their fees,
	// so this derivation is exact for any number of entry fills.
	// Falls back to the legacy (exit−entry)×qty formula when deployed_capital is missing.
	grossPnL := cp.RealizedPnL // legacy fallback
	totalCommission := exitCommission
	exitQty, _ := decimal.NewFromString(cp.Qty)
	exitPrice, _ := decimal.NewFromString(cp.ClosePrice)
	entryPrice, _ := decimal.NewFromString(cp.EntryPrice)
	netQty, _ := decimal.NewFromString(cp.Qty) // cp.Qty is set to snap.Qty (full leg qty)
	deployed, _ := decimal.NewFromString(cp.DeployedCapital)
	if deployed.IsPositive() && exitQty.IsPositive() && exitPrice.IsPositive() {
		// gross_pnl = pure price movement, no fees.
		//
		// Long  (cp.Side=="buy"):  bought at entryPrice, sold at exitPrice
		//   → PnL = exitQty×exitPrice − netQty×entryPrice
		//
		// Short (cp.Side=="sell"): sold at entryPrice, bought back at exitPrice
		//   → PnL = netQty×entryPrice − exitQty×exitPrice
		//
		// The legacy fallback (cp.RealizedPnL) is computed correctly in applyFill
		// using the closing fill's side, but the deployed-capital formula above used
		// the long formula unconditionally — inverting the sign for short trades.
		if cp.Side == "sell" { // short position: entry=sell, cover=buy
			grossPnL = netQty.Mul(entryPrice).Sub(exitQty.Mul(exitPrice)).String()
		} else { // long position: entry=buy, exit=sell
			grossPnL = exitQty.Mul(exitPrice).Sub(netQty.Mul(entryPrice)).String()
		}
		// entry_commission = deployed_capital − net_qty × entry_price.
		if entryPrice.IsPositive() {
			entryCommission := deployed.Sub(netQty.Mul(entryPrice))
			if entryCommission.IsPositive() {
				totalCommission = totalCommission.Add(entryCommission)
			}
		}
	}

	rec := perf.TradeRecord{
		HelmID:          h.helmID.String(),
		HandID:          h.id.String(),
		UserID:          h.helmRuntime.UserID.String(),
		Symbol:          cp.Symbol,
		Side:            cp.Side,
		Qty:             cp.Qty,
		Timeframe:       h.Timeframe,
		EntryPrice:      cp.EntryPrice,
		ExitPrice:       cp.ClosePrice,
		EntryAt:         cp.EntryAt,
		ExitAt:          exitAt,
		GrossPnL:        grossPnL,
		Commission:      decimalToString(totalCommission),
		StopLossPrice:   cp.StopLossPrice,
		TakeProfitPrice: cp.TakeProfitPrice,
		PlannedRisk:     plannedRisk(cp.Qty, cp.EntryPrice, cp.StopLossPrice),
		EntryOrderID:    cp.EntryOrderID,
		ExitOrderID:     cp.ExitOrderID,
		NEntries:        cp.NEntries,
		ExitReason:      string(cp.ExitReason),
		Strategy:        h.StrategyName,
	}
	if err := tl.Append(ctx, rec); err != nil {
		h.log.Warn("trade_log: publish failed", "err", err)
		return
	}

	// Poslog GC: trade record is now durable in JetStream — the poslog WAL for
	// this hand is no longer needed for crash recovery. Purge it to keep the
	// HELM_POSITIONS stream lean.
	//
	// Note: KindPositionOrphaned and KindOrderCancelled also land in the poslog
	// but do not go through the fill path, so they are not purged here. They
	// accumulate (2–3 events at most per cycle) and are cleaned up on the next
	// normal trade close. This is intentional — correctness is not affected.
	//
	// GC runs AFTER trade publish (not in applyFill) to avoid a race where a new
	// position opens in the same signal cycle: by the time appendTradeRecord is
	// called, KindPositionClosed has already been applied to h.pos, so any new
	// KindOrderPlaced that arrives concurrently will be published AFTER this point.
	// We re-check IsFlat() inside the goroutine as a final guard; if a new leg
	// opened in the narrow window between here and PurgeHand, we abort.
	if pl := h.helmRuntime.PosLog; pl != nil {
		helmID := h.helmID.String()
		handID := h.id.String()
		go func() {
			defer safe.Recover()
			h.mu.RLock()
			stillFlat := h.pos.IsFlat()
			h.mu.RUnlock()
			if !stillFlat {
				return
			}
			if err := pl.PurgeHand(context.Background(), helmID, handID); err != nil {
				h.log.Warn("poslog: purge after trade record failed (non-fatal)", "err", err)
			} else {
				h.log.Info("poslog: purged after trade record published")
			}
		}()
	}
}

// appendOrphanTradeRecord writes an audit trade record for a position that has been
// disowned (orphaned) by the hand — i.e. the user cancelled the bracket OCO or the
// hand could not cleanly exit. The record has exit_price = "" and gross_pnl = "0"
// so downstream KPI aggregates (win rate, Sharpe) can skip it by filtering on
// exit_reason = "orphaned", while the audit trail (helm_id, hand_id, symbol, entry)
// remains visible in the trade history.
func (h *Hand) appendOrphanTradeRecord(ctx context.Context, leg *position.LegState, source string) {
	tl := h.helmRuntime.TradeLog
	if tl == nil {
		return
	}
	now := time.Now().UTC()
	rec := perf.TradeRecord{
		HelmID:     h.helmID.String(),
		HandID:     h.id.String(),
		UserID:     h.helmRuntime.UserID.String(),
		Symbol:     leg.Symbol,
		Side:       leg.Side,
		Qty:        leg.Qty.String(),
		Timeframe:  h.Timeframe,
		EntryPrice: leg.EntryPrice.String(),
		EntryAt:    leg.OpenedAt,
		ExitAt:     now,
		// exit_price omitted (unknown — position left at exchange).
		// gross_pnl = "0": neutral sentinel; downstream filters on exit_reason = "orphaned".
		GrossPnL:        "0",
		StopLossPrice:   decimalToString(leg.StopLoss),
		TakeProfitPrice: decimalToString(leg.TakeProfit),
		PlannedRisk:     plannedRisk(leg.Qty.String(), leg.EntryPrice.String(), leg.StopLoss.String()),
		NEntries:        leg.EntryCount(),
		ExitReason:      string(strategy.ExitKindOrphan),
		Strategy:        h.StrategyName,
	}
	if err := tl.Append(ctx, rec); err != nil {
		h.log.Warn("trade_log: orphan record publish failed", "symbol", leg.Symbol, "source", source, "err", err)
	}

	// Poslog GC: just like normal trades, once the orphan trade record is durable,
	// we purge the poslog if the hand is flat.
	if pl := h.helmRuntime.PosLog; pl != nil {
		helmID := h.helmID.String()
		handID := h.id.String()
		go func() {
			defer safe.Recover()
			h.mu.RLock()
			stillFlat := h.pos.IsFlat()
			h.mu.RUnlock()
			if !stillFlat {
				return
			}
			if err := pl.PurgeHand(context.Background(), helmID, handID); err != nil {
				h.log.Warn("poslog: purge after orphan trade record failed (non-fatal)", "err", err)
			} else {
				h.log.Info("poslog: purged after orphan trade record published")
			}
		}()
	}
}

// decimalToString returns "" for the zero value so the JSON wire format omits
// the field (matching the omitzero hints on TradeRecord).
func decimalToString(d decimal.Decimal) string {
	if d.IsZero() {
		return ""
	}
	return d.String()
}

// plannedRisk computes the dollar risk implied at entry: qty × |entry − stop_loss|.
// Returns "" when any input is missing — PG sees NULL and r_multiple stays NULL.
func plannedRisk(qtyStr, entryStr, stopStr string) string {
	if qtyStr == "" || entryStr == "" || stopStr == "" {
		return ""
	}
	qty, err1 := decimal.NewFromString(qtyStr)
	entry, err2 := decimal.NewFromString(entryStr)
	stop, err3 := decimal.NewFromString(stopStr)
	if err1 != nil || err2 != nil || err3 != nil {
		return ""
	}
	risk := entry.Sub(stop).Abs().Mul(qty.Abs())
	if !risk.IsPositive() {
		return ""
	}
	return risk.String()
}

// publishBracketPlaced writes KindBracketPlaced to the durable poslog so bracket
// order IDs survive a restart. Called from within the PlaceExitOrders goroutine.
// positionID is the leg's opening order_id (stable PositionID); orderIDs are the
// exchange-returned SL/TP order IDs.
func (h *Hand) publishBracketPlaced(ctx context.Context, positionID, symbol string, orderIDs []string) {
	if len(orderIDs) == 0 {
		return
	}
	payload, err := json.Marshal(poslog.BracketPlacedPayload{
		Symbol:   symbol,
		OrderIDs: orderIDs,
	})
	if err != nil {
		h.log.Error("publishBracketPlaced: marshal failed", "err", err)
		return
	}
	h.publishAndApply(ctx, poslog.Event{
		ID:         positionID + "_bracket_" + orderIDs[0],
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindBracketPlaced,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
}
