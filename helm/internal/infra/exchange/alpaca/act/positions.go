package act

import (
	"fmt"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// PositionInfo is a simplified view of an Alpaca position.
type PositionInfo struct {
	Symbol        string
	Qty           float64
	Side          string // "long" or "short"
	AvgEntryPrice float64
	MarketValue   float64
	CostBasis     float64
	UnrealizedPL  float64
	UnrealizedPct float64
	CurrentPrice  float64
	ChangeToday   float64
}

// GetPositions returns all open positions.
func (c *Client) GetPositions(creds exchange.Credentials) ([]PositionInfo, error) {
	positions, err := c.newSDK(creds).GetPositions()
	if err != nil {
		return nil, fmt.Errorf("alpaca get positions: %w", err)
	}
	result := make([]PositionInfo, len(positions))
	for i := range positions {
		result[i] = mapPosition(&positions[i])
	}
	return result, nil
}

// GetPosition returns the open position for a single symbol.
func (c *Client) GetPosition(creds exchange.Credentials, symbol string) (*PositionInfo, error) {
	pos, err := c.newSDK(creds).GetPosition(symbol)
	if err != nil {
		return nil, fmt.Errorf("alpaca get position %s: %w", symbol, err)
	}
	p := mapPosition(pos)
	return &p, nil
}

// ClosePosition liquidates a position (partially or fully).
func (c *Client) ClosePosition(creds exchange.Credentials, symbol string, qty decimal.Decimal) (*alpacasdk.Order, error) {
	req := alpacasdk.ClosePositionRequest{}
	if qty.IsPositive() {
		req.Qty = qty
	}
	order, err := c.newSDK(creds).ClosePosition(symbol, req)
	if err != nil {
		return nil, fmt.Errorf("alpaca close position %s: %w", symbol, err)
	}
	return order, nil
}

// CloseAllPositions liquidates all open positions.
func (c *Client) CloseAllPositions(creds exchange.Credentials, cancelOrders bool) ([]alpacasdk.Order, error) {
	orders, err := c.newSDK(creds).CloseAllPositions(alpacasdk.CloseAllPositionsRequest{
		CancelOrders: cancelOrders,
	})
	if err != nil {
		return nil, fmt.Errorf("alpaca close all positions: %w", err)
	}
	return orders, nil
}

func mapPosition(p *alpacasdk.Position) PositionInfo {
	pi := PositionInfo{
		Symbol:        p.Symbol,
		Qty:           p.Qty.InexactFloat64(),
		Side:          p.Side,
		AvgEntryPrice: p.AvgEntryPrice.InexactFloat64(),
		CostBasis:     p.CostBasis.InexactFloat64(),
	}
	if p.MarketValue != nil {
		pi.MarketValue = p.MarketValue.InexactFloat64()
	}
	if p.UnrealizedPL != nil {
		pi.UnrealizedPL = p.UnrealizedPL.InexactFloat64()
	}
	if p.UnrealizedPLPC != nil {
		pi.UnrealizedPct = p.UnrealizedPLPC.InexactFloat64()
	}
	if p.CurrentPrice != nil {
		pi.CurrentPrice = p.CurrentPrice.InexactFloat64()
	}
	if p.ChangeToday != nil {
		pi.ChangeToday = p.ChangeToday.InexactFloat64()
	}
	return pi
}
