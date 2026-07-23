package actor

import (
	"context"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor/core/portfolio"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/core/tactics"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	helmdomain "mallow/helm/internal/module/helm/domain"
)

// TradeProposal is a hand's request for account-level trade validation.
type TradeProposal struct {
	HandID string
	Symbol string
	Intent strategy.Intent
	Price  decimal.Decimal // optional: resolved from price cache when zero
	ATR    decimal.Decimal
	// EquityOverride, when positive, replaces portfolio equity for tactician sizing.
	// Hands with AllocatedCapital pass their realized equity (allocated + cumPnL)
	// so position sizes compound with the hand's actual performance.
	EquityOverride decimal.Decimal
	// AvailableBudget, when positive, is the hand's hard cap on per-entry notional —
	// the tactician clamps qty so qty*price ≤ AvailableBudget. Zero disables the cap.
	// Set only for allocated hands; shared-pool hands rely on helm-level risk guards.
	AvailableBudget decimal.Decimal
	// PositionQty is the per-hand qty from poslog (h.pos.ActiveLegs), not net portfolio.
	// Used by the tactician to size exits and scale-outs correctly when multiple hands
	// share the same symbol on this helm.
	PositionQty decimal.Decimal
}

// processTrade validates a trade against account-level guards and sizes via the hand's
// tactician. Runs only on the trade actor goroutine (runTradeActor) — see ProcessTrade
// (helm_actor.go) for the public request/reply wrapper every hand actually calls.
func (r *HelmRuntime) processTrade(
	proposal TradeProposal,
	tact tactics.Planner,
) helmdomain.TradeReply {
	if proposal.Intent.Action == strategy.ActionDoNothing {
		return helmdomain.TradeReply{Approved: false, Reason: "strategy: do_nothing"}
	}

	count := r.requestCount.Add(1)
	if count > 100 {
		return helmdomain.TradeReply{Approved: false, Reason: "circuit breaker: too many requests"}
	}

	price := proposal.Price
	if price.IsZero() {
		price = r.lastKnownPrice(proposal.Symbol)
	}
	if price.IsZero() {
		if pf, ok := r.Exchange.(exchange.PriceFetcher); ok {
			// Fallback to REST only when WebSocket price cache is cold.
			// proposal.Symbol is already a bare ticker (dispatcher strips the herald prefix).
			// This call may take 100ms–2s — runs on the trade actor goroutine, so it does
			// block other hands' ProcessTrade/ReportFill calls for its duration. Acceptable:
			// a cold price cache is rare (first trade for a symbol) and the same tradeoff
			// existed before (this ran inside tradeMu too).
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			p, err := pf.GetCurrentPrice(ctx, r.Creds, proposal.Symbol)
			cancel()
			if err == nil && p.IsPositive() {
				price = p
				r.MarketData.SetPrice(proposal.Symbol, p)
			}
		}
	}
	if price.IsZero() {
		return helmdomain.TradeReply{Approved: false, Reason: "no price available for " + proposal.Symbol}
	}

	wasHalted := r.RiskMgr.IsHalted()
	if ok, reason := r.RiskMgr.Validate(proposal.Intent, proposal.HandID); !ok {
		if !wasHalted && r.RiskMgr.IsHalted() {
			// Risk manager just tripped — emit event to helm.events.{helmID}.
			// eventlog.Persister consumes that stream and updates helms.status = 'halted'.
			r.EmitEvent(natsapi.HelmEvent{
				Code:   CodeHelmHalted,
				Reason: reason,
				Msg:    "helm: halted by risk manager",
			})
		}
		return helmdomain.TradeReply{Approved: false, Reason: "risk: " + reason}
	}

	posQty := proposal.PositionQty
	equity := r.Portfolio.Equity()
	if proposal.EquityOverride.IsPositive() {
		equity = proposal.EquityOverride
	}
	tact.UpdateEquity(equity)
	plan := tact.Plan(proposal.Intent, tactics.MarketContext{
		Price:           price,
		ATR:             proposal.ATR,
		PositionQty:     posQty,
		AvailableBudget: proposal.AvailableBudget,
	})

	if !plan.Qty.IsPositive() && !plan.QuoteQty.IsPositive() {
		return helmdomain.TradeReply{Approved: false, Reason: "tactics: zero quantity after sizing"}
	}

	logArgs := []any{
		"hand_id", proposal.HandID,
		"symbol", proposal.Symbol,
		"action", proposal.Intent.Action,
		"side", plan.Side,
		"price", price,
	}
	if plan.QuoteQty.IsPositive() {
		logArgs = append(logArgs, "quote_qty", plan.QuoteQty)
	} else {
		logArgs = append(logArgs, "qty", plan.Qty)
	}
	slog.Info("runtime: trade approved", logArgs...)

	return helmdomain.TradeReply{
		Approved:     true,
		Qty:          plan.Qty,
		QuoteQty:     plan.QuoteQty,
		Side:         plan.Side,
		EntryType:    string(plan.EntryType),
		TIF:          string(plan.TIF),
		LimitPrice:   plan.LimitPrice,
		StopLoss:     plan.StopLoss,
		TakeProfit:   plan.TakeProfit,
		TrailingStop: plan.TrailingStop,
	}
}

// reportFill is the single choke-point for updating portfolio state after any fill.
// Runs only on the trade actor goroutine (runTradeActor) — see ReportFill (helm_actor.go)
// for the public fire-and-forget wrapper every caller actually uses.
//
// It is called from three paths:
//   - hand.applyFill        — normal hand-owned fill after hand has updated its own state
//   - runOrderProcessor     — orphan fill (hand removed between order and fill) or partial fill
//   - RecoverGapFills       — replaying fills missed during a crash/restart window
//
// All three paths must converge here so portfolio cash+positions stay consistent
// regardless of whether the owning hand is alive. Hand-level concerns (exit levels,
// poslog, metrics) are handled by the hand before calling here; this function owns
// only the helm-level aggregate state.
func (r *HelmRuntime) reportFill(fill helmdomain.FillReport) {
	// Fill price is the freshest known price; update cache so the next
	// ProcessTrade sizing call doesn't fall back to a stale tick.
	if fill.Price.IsPositive() {
		r.MarketData.SetPrice(fill.Symbol, fill.Price)
	}

	pfSide := portfolio.SideBuy
	if fill.Side == "sell" {
		pfSide = portfolio.SideSell
	}

	// Apply to portfolio: adjusts cash and positions. This is what makes
	// subsequent ProcessTrade calls see the correct open exposure.
	r.Portfolio.ApplyFill(portfolio.Fill{
		Timestamp:  fill.Timestamp,
		HandID:     fill.HandID,
		Symbol:     fill.Symbol,
		Side:       pfSide,
		Qty:        fill.Qty,
		Price:      fill.Price,
		Commission: fill.Commission,
	})

	r.MarkSyncDirty()
}

// syncDebounce coalesces a burst of fills (e.g. pyramid stacking) into a single
// post-order REST sync, and delays it enough for exchange settlement so ApplySync
// reconciles fees/rounding instead of clobbering a not-yet-acknowledged fill.
const syncDebounce = 3 * time.Second

// MarkSyncDirty schedules a single debounced REST sync after an order. Coalesced:
// concurrent fills within the debounce window collapse into one sync. The optimistic
// portfolio state (ApplyFill) covers reads until the sync lands.
func (r *HelmRuntime) MarkSyncDirty() {
	if !r.syncScheduled.CompareAndSwap(0, 1) {
		return // a sync is already scheduled for this window
	}
	time.AfterFunc(syncDebounce, func() {
		r.syncScheduled.Store(0)
		select {
		case <-r.stopCh:
			return // runtime stopped
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := r.Sync(ctx); err != nil {
			slog.Warn("helm: post-order sync failed", "helm_id", r.HelmID, "err", err)
			return
		}
		r.PersistSyncTime()
	})
}
