package signalfollower

import (
	"testing"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

func TestTruncateQty(t *testing.T) {
	cases := []struct {
		symbol  string
		rawStep string
		rawQty  string
		want    string
	}{
		{"SOLUSDT (Binance)", "0.01000000", "3.14159265", "3.14"},
		{"SOLUSDT (OKX)", "0.01", "3.14159265", "3.14"},
		{"ETHUSDT", "0.00010000", "0.12345678", "0.1234"},
		{"BTCUSDT", "0.00001000", "0.000049999", "0.00004"},
		{"XRPUSDT", "0.10000000", "99.99", "99.9"},
		{"DOGEUSDT", "1.00000000", "152.7", "152"},
		{"SOLUSDT_PERP", "0.10000000", "5.678", "5.6"},
		// fallback: no filter
		{"UNKNOWN", "0", "1.23456789", "1.23456789"},
	}

	for _, tc := range cases {
		step, _ := decimal.NewFromString(tc.rawStep)
		qty, _ := decimal.NewFromString(tc.rawQty)
		filters := exchange.SymbolFilters{QtyStep: step}

		got := TruncateQty(filters, qty)
		ok := got.String() == tc.want
		mark := "✅"
		if !ok {
			mark = "❌"
			t.Errorf("TruncateQty [%s] step=%s qty=%s: got %s, want %s",
				tc.symbol, tc.rawStep, tc.rawQty, got, tc.want)
		}

		// Sanity: result must be a valid multiple of step
		if step.IsPositive() {
			if !got.Mod(step).IsZero() {
				t.Errorf("  ⚠️  [%s] %s mod %s != 0", tc.symbol, got, step)
			}
		}

		t.Logf("%s [%-22s] step=%-12s qty=%-16s → %s", mark, tc.symbol, tc.rawStep, tc.rawQty, got)
	}
}
