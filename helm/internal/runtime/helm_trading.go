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
			if p, err := pf.GetCurrentPrice(ctx, r.Creds, proposal.Symbol); err == nil && p.IsPositive() {
				price = p
				r.pricesMu.Lock()
				r.prices[proposal.Symbol] = p
				r.pricesMu.Unlock()
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
			r.EmitEvent(natsapi.HelmEvent{
				Code:   CodeHelmHalted,
				Reason: reason,
				Msg:    "helm: halted by risk manager",
			})
		}
		return helmdomain.TradeReply{Approved: false, Reason: "risk: " + reason}
	}

	posQty := decimal.Zero
	if pos := r.Portfolio.GetPosition(proposal.Symbol); pos != nil {
		posQty = pos.Qty
	}
	equity := r.Portfolio.Equity()
	if proposal.EquityOverride.IsPositive() {
		equity = proposal.EquityOverride
	}
	tact.UpdateEquity(equity)
	plan := tact.Plan(proposal.Intent, tactics.MarketContext{
		Price:       price,
		ATR:         proposal.ATR,
		PositionQty: posQty,
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
		r.pricesMu.Lock()
		r.prices[fill.Symbol] = fill.Price
		r.pricesMu.Unlock()
	}

	pfSide := portfolio.SideBuy
	if fill.Side == "sell" {
		pfSide = portfolio.SideSell
	}

	now := time.Now().UTC()
	// Apply to portfolio: adjusts cash and positions. This is what makes
	// subsequent ProcessTrade calls see the correct open exposure.
	r.Portfolio.ApplyFill(portfolio.Fill{
		Timestamp:  fill.Timestamp,
		HandID:     fill.HandID,
		Symbol:     fill.Symbol,
		Side:       pfSide,
		Qty:        fill.Qty,
		Price:      fill.Price,
		Commission: decimal.Zero,
	})
	// Record an equity data point so RiskMgr can evaluate maxDrawdown on the next trade.
	r.Portfolio.RecordEquity(now)

	// Snapshot while still under tradeMu so cash+positions are consistent.
	// Published async below so NATS I/O does not block the lock.
	var snap *perf.Snapshot
	if r.SnapshotLog != nil {
		snap = r.helmSnapshot(now)
	}

	r.tradeMu.Unlock()

	// Append snapshot async: JetStream publish is slow relative to trade decisions.
	if snap != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.SnapshotLog.Append(ctx, *snap); err != nil {
				slog.Warn("snapshot_log: helm snapshot append failed", "helm_id", r.HelmID, "err", err)
			}
		}()
	}
}

// helmSnapshot builds a helm-level PortfolioSnapshot from current portfolio state.
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
		HelmID:    r.HelmID.String(),
		TS:        ts,
		Cash:      r.Portfolio.Cash(),
		Equity:    r.Portfolio.Equity(),
		Positions: entries,
	}
}
