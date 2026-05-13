package runtime

import (
	"context"
	"log/slog"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	orchdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/core/tactics"
)

// TradeProposal is a hand's request for account-level trade validation.
type TradeProposal struct {
	BotID  string
	Symbol string
	Intent strategy.Intent
	Price  decimal.Decimal // optional: resolved from price cache when zero
	ATR    decimal.Decimal
}

// ProcessTrade validates a trade against account-level guards and sizes via the hand's tactician.
func (r *HelmRuntime) ProcessTrade(
	ctx context.Context,
	proposal TradeProposal,
	tact tactics.Planner,
) orchdomain.TradeReply {
	if proposal.Intent.Action == strategy.ActionDoNothing {
		return orchdomain.TradeReply{Approved: false, Reason: "strategy: do_nothing"}
	}

	count := r.requestCount.Add(1)
	if count > 100 {
		return orchdomain.TradeReply{Approved: false, Reason: "circuit breaker: too many requests"}
	}

	r.tradeMu.Lock()
	defer r.tradeMu.Unlock()

	if ok, reason := r.RiskMgr.Validate(proposal.Intent); !ok {
		return orchdomain.TradeReply{Approved: false, Reason: "risk: " + reason}
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
		return orchdomain.TradeReply{Approved: false, Reason: "no price available for " + proposal.Symbol}
	}

	posQty := decimal.Zero
	if pos := r.Portfolio.GetPosition(proposal.Symbol); pos != nil {
		posQty = pos.Qty
	}
	tact.UpdateEquity(r.Portfolio.Equity())
	plan := tact.Plan(proposal.Intent, tactics.MarketContext{
		Price:       price,
		ATR:         proposal.ATR,
		PositionQty: posQty,
	})

	if !plan.Qty.IsPositive() {
		return orchdomain.TradeReply{Approved: false, Reason: "tactics: zero quantity after sizing"}
	}

	slog.Info("runtime: trade approved",
		"hand_id", proposal.BotID,
		"symbol", proposal.Symbol,
		"action", proposal.Intent.Action,
		"side", plan.Side,
		"qty", plan.Qty,
		"price", price,
	)

	return orchdomain.TradeReply{
		Approved:     true,
		Qty:          plan.Qty,
		Side:         plan.Side,
		EntryType:    string(plan.EntryType),
		LimitPrice:   plan.LimitPrice,
		StopLoss:     plan.StopLoss,
		TakeProfit:   plan.TakeProfit,
		TrailingStop: plan.TrailingStop,
	}
}

// ReportFill applies a fill to the portfolio and removes the order from the orderbook.
func (r *HelmRuntime) ReportFill(fill orchdomain.FillReport) {
	r.tradeMu.Lock()
	defer r.tradeMu.Unlock()

	r.OrderBook.RemoveOrder(fill.OrchestratorID, fill.OrderID)

	if fill.Price.IsPositive() {
		r.pricesMu.Lock()
		r.prices[fill.Symbol] = fill.Price
		r.pricesMu.Unlock()
	}

	pfSide := portfolio.SideBuy
	if fill.Side == "sell" {
		pfSide = portfolio.SideSell
	}

	r.Portfolio.ApplyFill(portfolio.Fill{
		Timestamp:  fill.Timestamp,
		Symbol:     fill.Symbol,
		Side:       pfSide,
		Qty:        fill.Qty,
		Price:      fill.Price,
		Commission: decimal.Zero,
	})

	slog.Info("runtime: fill applied",
		"hand_id", fill.BotID,
		"symbol", fill.Symbol,
		"side", fill.Side,
		"qty", fill.Qty,
		"price", fill.Price,
	)
}
