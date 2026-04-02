package ibkr

import (
	"context"
	"fmt"
)

// AccountSummary represents the IBKR account summary.
type AccountSummary struct {
	AccountID      string
	NetLiquidation float64
	TotalCashValue float64
	BuyingPower    float64
	GrossPosition  float64
}

// PositionInfo represents an IBKR portfolio position.
type PositionInfo struct {
	ConID         int
	ContractDesc  string
	Position      float64
	MarketPrice   float64
	MarketValue   float64
	AvgCost       float64
	UnrealizedPnL float64
	RealizedPnL   float64
}

// ContractInfo represents an IBKR contract search result.
type ContractInfo struct {
	ConID       int    `json:"conid"`
	CompanyName string `json:"companyName"`
	Symbol      string `json:"symbol"`
	SecType     string `json:"secType"`
	Exchange    string `json:"exchange"`
}

// GetAccounts returns the list of account IDs.
func (c *Client) GetAccounts(ctx context.Context) ([]string, error) {
	var resp struct {
		Accounts []string `json:"accounts"`
	}
	if err := c.doRequest(ctx, "GET", "/iserver/accounts", nil, &resp); err != nil {
		return nil, fmt.Errorf("ibkr get accounts: %w", err)
	}
	return resp.Accounts, nil
}

// GetAccountSummary returns the account summary.
func (c *Client) GetAccountSummary(ctx context.Context) (*AccountSummary, error) {
	path := fmt.Sprintf("/portfolio/%s/summary", c.cfg.AccountID)

	var resp map[string]struct {
		Amount float64 `json:"amount"`
	}
	if err := c.doRequest(ctx, "GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("ibkr account summary: %w", err)
	}

	return &AccountSummary{
		AccountID:      c.cfg.AccountID,
		NetLiquidation: resp["netliquidation"].Amount,
		TotalCashValue: resp["totalcashvalue"].Amount,
		BuyingPower:    resp["buyingpower"].Amount,
		GrossPosition:  resp["grosspositionvalue"].Amount,
	}, nil
}

// GetPositions returns all portfolio positions.
func (c *Client) GetPositions(ctx context.Context) ([]PositionInfo, error) {
	path := fmt.Sprintf("/portfolio/%s/positions/0", c.cfg.AccountID)

	var resp []struct {
		ConID         int     `json:"conid"`
		ContractDesc  string  `json:"contractDesc"`
		Position      float64 `json:"position"`
		MktPrice      float64 `json:"mktPrice"`
		MktValue      float64 `json:"mktValue"`
		AvgCost       float64 `json:"avgCost"`
		UnrealizedPnl float64 `json:"unrealizedPnl"`
		RealizedPnl   float64 `json:"realizedPnl"`
	}
	if err := c.doRequest(ctx, "GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("ibkr positions: %w", err)
	}

	results := make([]PositionInfo, len(resp))
	for i, p := range resp {
		results[i] = PositionInfo{
			ConID:         p.ConID,
			ContractDesc:  p.ContractDesc,
			Position:      p.Position,
			MarketPrice:   p.MktPrice,
			MarketValue:   p.MktValue,
			AvgCost:       p.AvgCost,
			UnrealizedPnL: p.UnrealizedPnl,
			RealizedPnL:   p.RealizedPnl,
		}
	}
	return results, nil
}

// SearchContracts searches for contracts by symbol (returns conid).
func (c *Client) SearchContracts(ctx context.Context, symbol string) ([]ContractInfo, error) {
	body := map[string]string{"symbol": symbol}
	var resp []ContractInfo
	if err := c.doRequest(ctx, "POST", "/iserver/secdef/search", body, &resp); err != nil {
		return nil, fmt.Errorf("ibkr search contracts: %w", err)
	}
	return resp, nil
}

// CheckAuthStatus verifies the IB Gateway authentication status.
func (c *Client) CheckAuthStatus(ctx context.Context) (bool, error) {
	var resp struct {
		Authenticated bool `json:"authenticated"`
		Connected     bool `json:"connected"`
	}
	if err := c.doRequest(ctx, "POST", "/iserver/auth/status", nil, &resp); err != nil {
		return false, fmt.Errorf("ibkr auth status: %w", err)
	}
	return resp.Authenticated && resp.Connected, nil
}
