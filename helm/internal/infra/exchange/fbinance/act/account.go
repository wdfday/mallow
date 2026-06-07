package act

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// GetSymbolFilters returns USDM futures symbol rules from /fapi/v1/exchangeInfo.
func (c *Client) GetSymbolFilters(ctx context.Context, symbol string) (exchange.SymbolFilters, error) {
	info, err := c.newFut(exchange.Credentials{}).NewExchangeInfoService().Do(ctx)
	if err != nil {
		return exchange.SymbolFilters{}, fmt.Errorf("fbinance exchange info (%s): %w", symbol, err)
	}
	for _, s := range info.Symbols {
		if s.Symbol != symbol {
			continue
		}
		f := exchange.SymbolFilters{
			BaseAsset:  s.BaseAsset,
			QuoteAsset: s.QuoteAsset,
		}
		if lot := s.LotSizeFilter(); lot != nil {
			f.QtyStep, _ = decimal.NewFromString(lot.StepSize)
			f.MinQty, _ = decimal.NewFromString(lot.MinQuantity)
		}
		if pf := s.PriceFilter(); pf != nil {
			f.PriceTick, _ = decimal.NewFromString(pf.TickSize)
		}
		if nf := s.MinNotionalFilter(); nf != nil {
			f.MinNotional, _ = decimal.NewFromString(nf.Notional)
		}
		return f, nil
	}
	return exchange.SymbolFilters{}, fmt.Errorf("fbinance: symbol %s not found", symbol)
}
