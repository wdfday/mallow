package runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
	"mallow/helm/internal/runtime/perf"
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

// ProcessTrade validates a trade against account-level guards and sizes via the hand's tactician.
//
// Price resolution is intentionally performed BEFORE acquiring tradeMu so that
// a slow or missing exchange REST call never blocks ReportFill (which also needs
// tradeMu) for other hands on the same helm.
func (r *HelmRuntime) ProcessTrade(
	ctx context.Context,
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

	// ── Resolve price BEFORE acquiring tradeMu ────────────────────────────────
	// lastKnownPrice and pricesMu are independent from tradeMu, so this does not
	// block concurrent ReportFill calls from other hands.
	price := proposal.Price
	if price.IsZero() {
		price = r.lastKnownPrice(proposal.Symbol)
	}
	if price.IsZero() {
		if pf, ok := r.Exchange.(exchange.PriceFetcher); ok {
			// Fallback to REST only when WebSocket price cache is cold.
			// This call may take 100ms–2s but runs outside tradeMu.
			// Strip exchange prefix ("binance:ETHUSDT" → "ETHUSDT") before
			// calling the REST ticker — exchange APIs never use the prefix.
			restSymbol := stripExchangePrefix(proposal.Symbol)
			if p, err := pf.GetCurrentPrice(ctx, r.Creds, restSymbol); err == nil && p.IsPositive() {
				price = p
				r.prices.set(proposal.Symbol, p) // stores raw + prefix-stripped key
			}
		}
	}
	if price.IsZero() {
		return helmdomain.TradeReply{Approved: false, Reason: "no price available for " + proposal.Symbol}
	}

	// ── Critical section: risk check + portfolio read + sizing ────────────────
	r.tradeMu.Lock()
	defer r.tradeMu.Unlock()

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

// ReportFill is the single choke-point for updating portfolio state after any fill.
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
func (r *HelmRuntime) ReportFill(fill helmdomain.FillReport) {
	r.tradeMu.Lock()

	// Fill price is the freshest known price; update cache so the next
	// ProcessTrade sizing call doesn't fall back to a stale tick.
	if fill.Price.IsPositive() {
		r.prices.set(fill.Symbol, fill.Price)
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

	// After a SELL fill, if the remaining position qty is ≤ the recorded dust
	// (sub-lot residual from lot-size truncation before placing the exit order),
	// the remaining position is untradeable. Remove it so the portfolio stays flat.
	if fill.Side == "sell" {
		if dust := r.GetDust(fill.Symbol); dust.IsPositive() {
			if pos := r.Portfolio.GetPosition(fill.Symbol); pos != nil && !pos.Qty.GreaterThan(dust) {
				r.Portfolio.RemovePosition(fill.Symbol)
			}
		}
	}

	r.tradeMu.Unlock()

	// Fills do NOT touch the equity curve / drawdown peak — those are a sampled
	// projection owned by the SnapshotWorker (after-order debounce + 60s heartbeat),
	// not an event-sourced aggregate. Fills only emit timing hints:
	//   MarkSnapshotDirty → worker samples equity (RecordEquity) + emits within 500ms.
	//   MarkSyncDirty     → debounced REST sync (~3s) reconciles cash/positions to the
	//                       exchange truth (settlement-safe; optimistic state covers the gap).
	r.MarkSnapshotDirty()
	r.MarkSyncDirty()
}

// helmSnapshot builds a helm-level Snapshot from current portfolio state.
// Caller must hold tradeMu (or otherwise ensure portfolio is not being written concurrently).
func (r *HelmRuntime) helmSnapshot(ts time.Time) *perf.Snapshot {
	rawPos := r.Portfolio.Positions()
	entries := make([]perf.PositionEntry, 0, len(rawPos))
	for _, p := range rawPos {
		side := "buy"
		if p.Qty.IsNegative() {
			side = "sell"
		}
		entries = append(entries, perf.PositionEntry{
			Symbol:   p.Symbol,
			Side:     side,
			Qty:      p.Qty.Abs(),
			AvgPrice: p.AvgPrice,
		})
	}
	return &perf.Snapshot{
		HelmID:        r.HelmID.String(),
		TS:            ts,
		Cash:          r.Portfolio.Cash(),
		Equity:        r.Portfolio.Equity(),
		RealizedPnL:   r.Portfolio.RealizedPnL(),
		UnrealizedPnL: r.Portfolio.UnrealizedPnL(),
		Positions:     entries,
	}
}

// MarkSnapshotDirty hints to the SnapshotWorker that a fill just occurred.
// The worker will flush a snapshot within snapshotDebounce (500ms).
// This is a timing hint only — snapshot correctness does not depend on it.
func (r *HelmRuntime) MarkSnapshotDirty() {
	r.snapshotDirty.Store(1)
}

// RecordEquity samples the current equity into the equity curve and advances the
// drawdown high-water mark. Called by the SnapshotWorker on its cadence (post-fill
// debounce + 60s heartbeat) — NOT from the fill path — so the drawdown peak is a
// time-sampled projection, not driven by any single fill's instantaneous mark-to-market.
func (r *HelmRuntime) RecordEquity(ts time.Time) {
	r.Portfolio.RecordEquity(ts)
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
		r.persistSyncTime()
	})
}

// BuildSnapshot returns the current helm-level portfolio snapshot.
// Acquires tradeMu so cash+positions are consistent.
// Called by SnapshotWorker; safe from any goroutine.
func (r *HelmRuntime) BuildSnapshot(ts time.Time) *perf.Snapshot {
	r.tradeMu.Lock()
	snap := r.helmSnapshot(ts)
	r.tradeMu.Unlock()
	return snap
}

// HelmStringID returns the helm ID as a string for use in NATS subjects.
// Implements perflog.SnapshotEmitter.
func (r *HelmRuntime) HelmStringID() string {
	return r.HelmID.String()
}

// ── Dust management ───────────────────────────────────────────────────────────

// RecordDust adds qty to the known dust residual for symbol.
// Called after a spot exit order is placed with truncated qty so the sub-step
// remainder is not mistaken for an external close by checkPositionDesync.
func (r *HelmRuntime) RecordDust(symbol string, qty decimal.Decimal) { r.dust.record(symbol, qty) }

// ClearDust removes all recorded dust for symbol.
// Called when a new position opens for the symbol (dust from the previous trade
// is no longer relevant and should not suppress future desync detection).
func (r *HelmRuntime) ClearDust(symbol string) { r.dust.clear(symbol) }

// GetDust returns the accumulated dust qty for symbol (zero if none recorded).
func (r *HelmRuntime) GetDust(symbol string) decimal.Decimal { return r.dust.get(symbol) }
