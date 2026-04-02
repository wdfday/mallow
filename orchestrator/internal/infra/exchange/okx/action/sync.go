package action

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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

	cash := 0.0
	type holding struct {
		ccy string
		qty float64
	}
	var nonCash []holding

	for _, b := range info.Balances {
		if b.Available <= 0 && b.Equity <= 0 {
			continue
		}
		qty := b.Available
		if b.Equity > qty {
			qty = b.Equity
		}
		if stablecoins[b.Currency] {
			cash += b.Available
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
			price = 0
		}
		positions = append(positions, exchange.ExchangePosition{
			Symbol:   instID,
			Qty:      h.qty,
			CurPrice: price,
		})
	}

	mv := 0.0
	for _, p := range positions {
		mv += p.Qty * p.CurPrice
	}

	return &exchange.AccountSnapshot{
		Cash:      cash,
		Equity:    cash + mv,
		Positions: positions,
	}, nil
}
