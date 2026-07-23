package signalfollower

import (
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// TruncateQty rounds qty DOWN to the nearest valid LOT_SIZE multiple.
func TruncateQty(filters exchange.SymbolFilters, qty decimal.Decimal) decimal.Decimal {
	if filters.QtyStep.IsPositive() {
		return qty.Div(filters.QtyStep).Floor().Mul(filters.QtyStep)
	}
	return qty.Truncate(8)
}

// PriceTick returns the number of meaningful decimal places for a step size.
// Normalises via float64 → shortest string to strip trailing zeros before counting.
func PriceTick(step decimal.Decimal) int32 {
	if !step.IsPositive() {
		return 2
	}
	f, _ := step.Float64()
	s := strconv.FormatFloat(f, 'f', -1, 64)
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return 0
	}
	prec := int32(len(s) - dot - 1)
	if prec > 8 {
		return 8
	}
	return prec
}
