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
// Queries USDM futures userTrades for each provided symbol.
func (c *Client) FilledOrders(ctx context.Context, creds exchange.Credentials, symbols []string, from, to time.Time) ([]exchange.AccountTransaction, error) {
	if len(symbols) == 0 {
		return nil, nil
	}

	fromMs := from.UnixMilli()
	toMs := to.UnixMilli()
	fut := c.newFut(creds)
	var all []exchange.AccountTransaction

	for _, sym := range symbols {
		trades, err := fut.NewListAccountTradeService().
			Symbol(sym).
			StartTime(fromMs).
			EndTime(toMs).
			Limit(1000).
			Do(ctx)
		if err != nil {
			slog.Warn("fbinance history: futures trades failed", "symbol", sym, "err", err)
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
				FeeAsset: t.CommissionAsset,
				FilledAt: time.UnixMilli(t.Time).UTC(),
			})
		}
	}

	return all, nil
}
