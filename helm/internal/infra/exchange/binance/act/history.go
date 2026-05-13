package act

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/adshao/go-binance/v2/futures"

	"mallow/helm/internal/infra/exchange"
)

// FilledOrders implements exchange.HistoryFetcher.
// Queries myTrades (spot) and userTrades (futures) for each provided symbol.
// symbols must be non-empty — Binance requires a per-symbol query.
func (c *Client) FilledOrders(ctx context.Context, creds exchange.Credentials, symbols []string, from, to time.Time) ([]exchange.AccountTransaction, error) {
	if len(symbols) == 0 {
		return nil, nil
	}

	fromMs := from.UnixMilli()
	toMs := to.UnixMilli()
	spot := c.newSpot(creds)
	var all []exchange.AccountTransaction

	for _, sym := range symbols {
		trades, err := spot.NewListTradesService().
			Symbol(sym).
			StartTime(fromMs).
			EndTime(toMs).
			Limit(1000).
			Do(ctx)
		if err != nil {
			slog.Warn("binance history: spot trades failed", "symbol", sym, "err", err)
			continue
		}
		for _, t := range trades {
			side := exchange.Buy
			if !t.IsBuyer {
				side = exchange.Sell
			}
			all = append(all, exchange.AccountTransaction{
				TradeID:  strconv.FormatInt(t.ID, 10),
				OrderID:  strconv.FormatInt(t.OrderID, 10),
				Symbol:   t.Symbol,
				Side:     string(side),
				Qty:      parseDecimal(t.Quantity),
				AvgPrice: parseDecimal(t.Price),
				Fee:      parseDecimal(t.Commission),
				FilledAt: time.UnixMilli(t.Time).UTC(),
			})
		}
	}

	// Only query USDM futures endpoint for futures_usdm and unified accounts.
	// futures_coinm uses a separate /dapi/ endpoint (not yet implemented); options and spot skip.
	if creds.AccountType != exchange.AccountFuturesUSDM && creds.AccountType != exchange.AccountUnified {
		return all, nil
	}

	fut := c.newFut(creds)
	for _, sym := range symbols {
		trades, err := fut.NewListAccountTradeService().
			Symbol(sym).
			StartTime(fromMs).
			EndTime(toMs).
			Limit(1000).
			Do(ctx)
		if err != nil {
			slog.Warn("binance history: futures trades failed", "symbol", sym, "err", err)
			continue
		}
		for _, t := range trades {
			side := exchange.Buy
			if t.Side == futures.SideTypeSell {
				side = exchange.Sell
			}
			all = append(all, exchange.AccountTransaction{
				TradeID:  fmt.Sprintf("fut-%d", t.ID),
				OrderID:  strconv.FormatInt(t.OrderID, 10),
				Symbol:   t.Symbol,
				Side:     string(side),
				Qty:      parseDecimal(t.Quantity),
				AvgPrice: parseDecimal(t.Price),
				Fee:      parseDecimal(t.Commission),
				FilledAt: time.UnixMilli(t.Time).UTC(),
			})
		}
	}

	return all, nil
}
