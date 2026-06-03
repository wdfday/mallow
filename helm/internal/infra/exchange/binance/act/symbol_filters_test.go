package act

import (
	"context"
	"fmt"
	"testing"
)

// TestSymbolFilters_Live calls the real Binance public ExchangeInfo endpoint
// and prints all SymbolFilters fields for every symbol in deployment/symbols.yaml.
// Run with: go test -v -run TestSymbolFilters_Live ./internal/infra/exchange/binance/act/
func TestSymbolFilters_Live(t *testing.T) {
	symbols := []string{
		"BTCUSDT", "ETHUSDT", "BNBUSDT", "SOLUSDT", "XRPUSDT",
		"ADAUSDT", "AVAXUSDT", "DOTUSDT", "LINKUSDT", "POLUSDT",
	}

	c := New(false)
	ctx := context.Background()

	fmt.Printf("%-10s  %-5s  %-6s  %-10s  %-10s  %-10s  %-12s\n",
		"symbol", "base", "quote", "qty_step", "price_tick", "min_qty", "min_notional")
	fmt.Println("-----------------------------------------------------------------------")

	for _, sym := range symbols {
		f, err := c.GetSymbolFilters(ctx, sym)
		if err != nil {
			t.Errorf("%s: %v", sym, err)
			continue
		}
		fmt.Printf("%-10s  %-5s  %-6s  %-10s  %-10s  %-10s  %-12s\n",
			sym, f.BaseAsset, f.QuoteAsset,
			f.QtyStep, f.PriceTick, f.MinQty, f.MinNotional)
	}
}
