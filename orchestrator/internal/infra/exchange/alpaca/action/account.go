package action

import (
	"fmt"
	"time"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
)

// AccountInfo is a simplified view of an Alpaca account.
type AccountInfo struct {
	ID               string
	Status           string
	Currency         string
	Cash             float64
	BuyingPower      float64
	Equity           float64
	PortfolioValue   float64
	LongMarketValue  float64
	ShortMarketValue float64
	PatternDayTrader bool
	TradingBlocked   bool
	DaytradeCount    int64
}

// AssetInfo is a simplified view of an Alpaca asset.
type AssetInfo struct {
	ID           string
	Symbol       string
	Name         string
	Exchange     string
	Class        string
	Tradable     bool
	Fractionable bool
	Shortable    bool
	Marginable   bool
}

// ClockInfo represents the current market clock state.
type ClockInfo struct {
	Timestamp time.Time
	IsOpen    bool
	NextOpen  time.Time
	NextClose time.Time
}

// GetAccount returns the current account information.
func (c *Client) GetAccount() (*AccountInfo, error) {
	acct, err := c.sdk.GetAccount()
	if err != nil {
		return nil, fmt.Errorf("alpaca get account: %w", err)
	}
	return &AccountInfo{
		ID:               acct.ID,
		Status:           acct.Status,
		Currency:         acct.Currency,
		Cash:             acct.Cash.InexactFloat64(),
		BuyingPower:      acct.BuyingPower.InexactFloat64(),
		Equity:           acct.Equity.InexactFloat64(),
		PortfolioValue:   acct.PortfolioValue.InexactFloat64(),
		LongMarketValue:  acct.LongMarketValue.InexactFloat64(),
		ShortMarketValue: acct.ShortMarketValue.InexactFloat64(),
		PatternDayTrader: acct.PatternDayTrader,
		TradingBlocked:   acct.TradingBlocked,
		DaytradeCount:    acct.DaytradeCount,
	}, nil
}

// GetAsset returns information about a single tradeable asset.
func (c *Client) GetAsset(symbol string) (*AssetInfo, error) {
	asset, err := c.sdk.GetAsset(symbol)
	if err != nil {
		return nil, fmt.Errorf("alpaca get asset %s: %w", symbol, err)
	}
	return mapAsset(asset), nil
}

// GetAssets returns a list of assets matching the given filters.
func (c *Client) GetAssets(req alpacasdk.GetAssetsRequest) ([]AssetInfo, error) {
	assets, err := c.sdk.GetAssets(req)
	if err != nil {
		return nil, fmt.Errorf("alpaca get assets: %w", err)
	}
	result := make([]AssetInfo, len(assets))
	for i := range assets {
		result[i] = *mapAsset(&assets[i])
	}
	return result, nil
}

// GetClock returns the current market clock (open/close times).
func (c *Client) GetClock() (*ClockInfo, error) {
	clock, err := c.sdk.GetClock()
	if err != nil {
		return nil, fmt.Errorf("alpaca get clock: %w", err)
	}
	return &ClockInfo{
		Timestamp: clock.Timestamp,
		IsOpen:    clock.IsOpen,
		NextOpen:  clock.NextOpen,
		NextClose: clock.NextClose,
	}, nil
}

// GetPortfolioHistory returns historical portfolio snapshots.
func (c *Client) GetPortfolioHistory(req alpacasdk.GetPortfolioHistoryRequest) (*alpacasdk.PortfolioHistory, error) {
	history, err := c.sdk.GetPortfolioHistory(req)
	if err != nil {
		return nil, fmt.Errorf("alpaca get portfolio history: %w", err)
	}
	return history, nil
}

func mapAsset(a *alpacasdk.Asset) *AssetInfo {
	return &AssetInfo{
		ID:           a.ID,
		Symbol:       a.Symbol,
		Name:         a.Name,
		Exchange:     a.Exchange,
		Class:        string(a.Class),
		Tradable:     a.Tradable,
		Fractionable: a.Fractionable,
		Shortable:    a.Shortable,
		Marginable:   a.Marginable,
	}
}
