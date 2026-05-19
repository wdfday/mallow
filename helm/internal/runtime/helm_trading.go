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

	r.tradeMu.Lock()
	defer r.tradeMu.Unlock()

	if ok, reason := r.RiskMgr.Validate(proposal.Intent); !ok {
		return helmdomain.TradeReply{Approved: false, Reason: "risk: " + reason}
	}

	price := proposal.Price
	if price.IsZero() {
		price = r.lastKnownPrice(proposal.Symbol)
	}
	if price.IsZero() {
		if pf, ok := r.Exchange.(exchange.PriceFetcher); ok {
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

// ReportFill applies a fill to the portfolio and removes the order from the tracking map.
// After applying, it records an equity point (for maxDrawdown) and publishes a helm-level
// portfolio snapshot to the PortfolioLog so the FE can compute equity at any timeframe.
func (r *HelmRuntime) ReportFill(fill helmdomain.FillReport) {
	r.tradeMu.Lock()

	r.RemoveOrderTracking(fill.OrderID)

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
	r.Portfolio.ApplyFill(portfolio.Fill{
		Timestamp:  fill.Timestamp,
		HandID:     fill.HandID,
		Symbol:     fill.Symbol,
		Side:       pfSide,
		Qty:        fill.Qty,
		Price:      fill.Price,
		Commission: decimal.Zero,
	})
	r.Portfolio.RecordEquity(now)

	// Snapshot for PortfolioLog: raw cash + positions (no price multiplication).
	var snap *perf.PortfolioSnapshot
	if r.PortfolioLog != nil {
		snap = r.helmSnapshot(now)
	}

	r.tradeMu.Unlock()

	r.EmitEvent(natsapi.HelmEvent{
		HandID:  fill.HandID,
		Code:    CodeOrderFilled,
		Symbol:  fill.Symbol,
		Side:    fill.Side,
		Qty:     fill.Qty,
		Price:   fill.Price,
		OrderID: fill.OrderID,
		Msg:     "runtime: fill applied to portfolio",
	})

	if snap != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := r.PortfolioLog.Append(ctx, *snap); err != nil {
				slog.Warn("portfolio_log: helm snapshot append failed", "helm_id", r.HelmID, "err", err)
			}
		}()
	}
}

// helmSnapshot builds a helm-level PortfolioSnapshot from current portfolio state.
// Caller must hold tradeMu (or otherwise ensure portfolio is not being written concurrently).
func (r *HelmRuntime) helmSnapshot(ts time.Time) *perf.PortfolioSnapshot {
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
	return &perf.PortfolioSnapshot{
		HelmID:    r.HelmID.String(),
		TS:        ts,
		Cash:      r.Portfolio.Cash(),
		Positions: entries,
	}
}
