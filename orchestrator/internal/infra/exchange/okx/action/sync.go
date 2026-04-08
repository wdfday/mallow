package action

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
)

var stablecoins = map[string]bool{
	"USDT": true,
	"USDC": true,
	"BUSD": true,
	"DAI":  true,
	"TUSD": true,
	"OKB":  false, // OKB is exchange token, not stablecoin
}

// SyncAccount implements exchange.AccountSyncer.
// Cash = sum of stablecoin available balances.
// Positions = non-stablecoin spot holdings priced via REST ticker.
func (c *Client) SyncAccount(ctx context.Context, _ *time.Time) (*exchange.AccountSnapshot, error) {
	info, err := c.GetBalance(ctx)
	if err != nil {
		return nil, fmt.Errorf("okx sync: get balance: %w", err)
	}

	cash := decimal.Zero
	type holding struct {
		ccy string
		qty decimal.Decimal
	}
	var nonCash []holding

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
		if stablecoins[b.Currency] {
			cash = cash.Add(available)
		} else {
			nonCash = append(nonCash, holding{ccy: b.Currency, qty: qty})
		}
	}

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

	mv := decimal.Zero
	for _, p := range positions {
		mv = mv.Add(p.Qty.Mul(p.CurPrice))
	}

	return &exchange.AccountSnapshot{
		Cash:      cash,
		Equity:    cash.Add(mv),
		Positions: positions,
	}, nil
}
