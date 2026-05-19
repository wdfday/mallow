package act

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

var stablecoins = map[string]bool{
	"USDT": true,
	"USDC": true,
	"BUSD": true,
	"DAI":  true,
	"TUSD": true,
	"OKB":  false,
}

// SyncAccount implements exchange.AccountSyncer.
func (c *Client) SyncAccount(ctx context.Context, creds exchange.Credentials, _ *time.Time) (*exchange.AccountSnapshot, error) {
	info, err := c.GetBalance(ctx, creds)
	if err != nil {
		return nil, fmt.Errorf("okx sync: get balance: %w", err)
	}

	cash := decimal.Zero
	type holding struct {
		ccy string
		qty decimal.Decimal
	}
	var nonCash []holding
	var balances []exchange.AssetBalance

	for _, b := range info.Balances {
		available := decimal.NewFromFloat(b.Available)
		equity := decimal.NewFromFloat(b.Equity)
		if !available.IsPositive() && !equity.IsPositive() {
			continue
		}
		qty := available
		if equity.GreaterThan(qty) {
			qty = equity
		}
		if available.IsPositive() {
			balances = append(balances, exchange.AssetBalance{Asset: b.Currency, Free: available})
		}
		if stablecoins[b.Currency] {
			cash = cash.Add(available)
		} else {
			nonCash = append(nonCash, holding{ccy: b.Currency, qty: qty})
		}
	}

	// Spot holdings from balance — no avgPx available from this endpoint.
	positions := make([]exchange.ExchangePosition, 0, len(nonCash))
	for _, h := range nonCash {
		instID := h.ccy + "-USDT"
		price, err := c.tickerLast(ctx, instID)
		if err != nil {
			slog.Warn("okx sync: failed to price asset", "ccy", h.ccy, "err", err)
			price = decimal.Zero
		}
		positions = append(positions, exchange.ExchangePosition{
			Symbol:   instID,
			Qty:      h.qty,
			CurPrice: price,
		})
	}

	// Derivatives positions (swap/futures) — has avgPx. Overlay or append.
	if derivs, err := c.GetPositions(ctx, creds, ""); err != nil {
		slog.Warn("okx sync: failed to get derivative positions", "err", err)
	} else {
		spotIdx := make(map[string]int, len(positions))
		for i, p := range positions {
			spotIdx[p.Symbol] = i
		}
		for _, d := range derivs {
			avgPx := decimal.NewFromFloat(d.AvgPx)
			markPx := decimal.NewFromFloat(d.MarkPx)
			qty := decimal.NewFromFloat(d.Pos)
			if qty.IsZero() {
				continue
			}
			if i, ok := spotIdx[d.InstID]; ok {
				// Already in spot list — overlay avgPx from positions API.
				positions[i].AvgPrice = avgPx
				if markPx.IsPositive() {
					positions[i].CurPrice = markPx
				}
			} else {
				// Pure derivative not in balance (e.g. short, or coin-margined).
				positions = append(positions, exchange.ExchangePosition{
					Symbol:   d.InstID,
					Qty:      qty,
					AvgPrice: avgPx,
					CurPrice: markPx,
				})
			}
		}
	}

	mv := decimal.Zero
	for _, p := range positions {
		mv = mv.Add(p.Qty.Mul(p.CurPrice))
	}

	return &exchange.AccountSnapshot{
		Cash:      cash,
		Equity:    cash.Add(mv),
		Positions: positions,
		Balances:  balances,
	}, nil
}
