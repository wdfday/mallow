package domain_test

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"

	"mallow/investment/internal/module/position/domain"
)

const delta = 1e-9 // tolerance for floating-point comparisons

func d(f float64) decimal.Decimal { return decimal.NewFromFloat(f) }
func di(i int64) decimal.Decimal  { return decimal.NewFromInt(i) }

func newPosition() *domain.PortfolioPosition {
	return &domain.PortfolioPosition{
		Status:   "active",
		Currency: "USD",
	}
}

// 1. AddQuantity() first buy: quantity, avgCost, totalCost set correctly.
func TestAddQuantity_FirstBuy(t *testing.T) {
	p := newPosition()
	p.AddQuantity(di(10), d(100.0))

	assert.InDelta(t, 10.0, p.Quantity.InexactFloat64(), delta)
	assert.InDelta(t, 100.0, p.AvgCost.InexactFloat64(), delta)
	assert.InDelta(t, 1000.0, p.TotalCost.InexactFloat64(), delta)
}

// 2. AddQuantity() second buy at different price: avgCost recalculates as weighted average.
func TestAddQuantity_SecondBuyRecalculatesAvgCost(t *testing.T) {
	p := newPosition()
	p.AddQuantity(di(10), d(100.0)) // cost = 1000, qty = 10
	p.AddQuantity(di(10), d(200.0)) // cost = 2000, qty = 10 → total cost = 3000, qty = 20

	assert.InDelta(t, 20.0, p.Quantity.InexactFloat64(), delta)
	assert.InDelta(t, 3000.0, p.TotalCost.InexactFloat64(), delta)
	assert.InDelta(t, 150.0, p.AvgCost.InexactFloat64(), delta) // (1000 + 2000) / 20 = 150
}

// 3. AddQuantity() multiple buys: running avg cost stays correct.
func TestAddQuantity_MultipleBuysRunningAvgCost(t *testing.T) {
	p := newPosition()
	p.AddQuantity(di(5), d(100.0))  // cost 500,  qty 5,  avg 100
	p.AddQuantity(di(5), d(120.0))  // cost 600,  qty 5,  total cost 1100, qty 10, avg 110
	p.AddQuantity(di(10), d(110.0)) // cost 1100, qty 10, total cost 2200, qty 20, avg 110

	assert.InDelta(t, 20.0, p.Quantity.InexactFloat64(), delta)
	assert.InDelta(t, 2200.0, p.TotalCost.InexactFloat64(), delta)
	assert.InDelta(t, 110.0, p.AvgCost.InexactFloat64(), delta)
}

// 4. RemoveQuantity() partial sell: quantity decreases, realizedPnL correct, status stays active.
func TestRemoveQuantity_PartialSell(t *testing.T) {
	p := newPosition()
	p.AddQuantity(di(10), d(100.0)) // avgCost = 100, totalCost = 1000

	realized := p.RemoveQuantity(di(4), d(150.0)) // sell 4 @ 150 → proceeds 600, costBasis 400

	assert.InDelta(t, 200.0, realized.InexactFloat64(), delta) // 600 - 400
	assert.InDelta(t, 6.0, p.Quantity.InexactFloat64(), delta)
	assert.InDelta(t, 200.0, p.RealizedPnL.InexactFloat64(), delta)
	assert.Equal(t, "active", p.Status)
	assert.Nil(t, p.ClosedAt)
}

// 5. RemoveQuantity() full sell: status becomes "closed", ClosedAt set, quantity=0.
func TestRemoveQuantity_FullSell_ClosePosition(t *testing.T) {
	p := newPosition()
	p.AddQuantity(di(10), d(100.0))

	p.RemoveQuantity(di(10), d(120.0))

	assert.InDelta(t, 0.0, p.Quantity.InexactFloat64(), delta)
	assert.Equal(t, "closed", p.Status)
	assert.NotNil(t, p.ClosedAt)
}

// 6. RemoveQuantity() sell more than available: quantity is capped to available.
func TestRemoveQuantity_SellMoreThanAvailable_CappedToAvailable(t *testing.T) {
	p := newPosition()
	p.AddQuantity(di(5), d(100.0))

	realized := p.RemoveQuantity(di(20), d(120.0)) // only 5 available

	assert.InDelta(t, 0.0, p.Quantity.InexactFloat64(), delta)
	assert.Equal(t, "closed", p.Status)
	// realized = 5 * 120 - 5 * 100 = 600 - 500 = 100
	assert.InDelta(t, 100.0, realized.InexactFloat64(), delta)
}

// 7. RemoveQuantity() returns correct realized PnL (proceeds - cost basis).
func TestRemoveQuantity_ReturnsCorrectRealizedPnL(t *testing.T) {
	p := newPosition()
	p.AddQuantity(di(10), d(80.0)) // avgCost = 80, totalCost = 800

	realized := p.RemoveQuantity(di(5), d(100.0)) // proceeds = 500, costBasis = 400

	assert.InDelta(t, 100.0, realized.InexactFloat64(), delta)
}

// 8. CalculateMetrics(): CurrentValue = Quantity * CurrentPrice.
func TestCalculateMetrics_CurrentValue(t *testing.T) {
	p := newPosition()
	p.AddQuantity(di(10), d(100.0))
	p.CurrentPrice = d(130.0)
	p.CalculateMetrics()

	assert.InDelta(t, 1300.0, p.CurrentValue.InexactFloat64(), delta) // 10 * 130
}

// 9. CalculateMetrics(): UnrealizedPnL = CurrentValue - TotalCost.
func TestCalculateMetrics_UnrealizedPnL(t *testing.T) {
	p := newPosition()
	p.AddQuantity(di(10), d(100.0)) // TotalCost = 1000
	p.CurrentPrice = d(130.0)
	p.CalculateMetrics()

	assert.InDelta(t, 300.0, p.UnrealizedPnL.InexactFloat64(), delta) // 1300 - 1000
}

// 10. CalculateMetrics(): UnrealizedPct = (UnrealizedPnL / TotalCost) * 100.
func TestCalculateMetrics_UnrealizedPct(t *testing.T) {
	p := newPosition()
	p.AddQuantity(di(10), d(100.0)) // TotalCost = 1000
	p.CurrentPrice = d(130.0)
	p.CalculateMetrics()

	assert.InDelta(t, 30.0, p.UnrealizedPct, delta) // (300 / 1000) * 100
}

// 11. CalculateMetrics(): when TotalCost=0, UnrealizedPct stays 0 (no divide by zero).
func TestCalculateMetrics_ZeroTotalCost_NoDivideByZero(t *testing.T) {
	p := newPosition()
	// Do not add any quantity — TotalCost stays 0
	p.CurrentPrice = d(100.0)
	p.CalculateMetrics()

	assert.InDelta(t, 0.0, p.UnrealizedPct, delta)
}
