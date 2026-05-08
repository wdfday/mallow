package oanda

import (
	"context"
	"fmt"
	"math"

	"mallow/helm/internal/infra/exchange"
)

// AccountInfo is a simplified view of the OANDA account.
type AccountInfo struct {
	ID                string
	Currency          string
	Balance           float64
	NAV               float64
	UnrealizedPL      float64
	MarginUsed        float64
	MarginAvailable   float64
	OpenTradeCount    int
	OpenPositionCount int
}

// PositionInfo represents an open position on OANDA.
type PositionInfo struct {
	Instrument    string
	LongUnits     float64
	LongAvgPrice  float64
	LongPL        float64
	ShortUnits    float64
	ShortAvgPrice float64
	ShortPL       float64
	UnrealizedPL  float64
}

// InstrumentInfo represents a tradable OANDA instrument.
type InstrumentInfo struct {
	Name                string // e.g. "EUR_USD"
	DisplayName         string // e.g. "EUR/USD"
	Type                string // CURRENCY, CFD, METAL
	PipLocation         int
	TradeUnitsPrecision int
	MinTradeSize        string
}

// GetAccount returns the account information.
func (c *Client) GetAccount(ctx context.Context, creds exchange.Credentials) (*AccountInfo, error) {
	path := fmt.Sprintf("/v3/accounts/%s/summary", creds.AccountID)
	var resp struct {
		Account struct {
			ID                string `json:"id"`
			Currency          string `json:"currency"`
			Balance           string `json:"balance"`
			NAV               string `json:"NAV"`
			UnrealizedPL      string `json:"unrealizedPL"`
			MarginUsed        string `json:"marginUsed"`
			MarginAvailable   string `json:"marginAvailable"`
			OpenTradeCount    int    `json:"openTradeCount"`
			OpenPositionCount int    `json:"openPositionCount"`
		} `json:"account"`
	}

	if err := c.doRequest(ctx, creds, "GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("oanda get account: %w", err)
	}

	a := resp.Account
	return &AccountInfo{
		ID:                a.ID,
		Currency:          a.Currency,
		Balance:           parseFloat(a.Balance),
		NAV:               parseFloat(a.NAV),
		UnrealizedPL:      parseFloat(a.UnrealizedPL),
		MarginUsed:        parseFloat(a.MarginUsed),
		MarginAvailable:   parseFloat(a.MarginAvailable),
		OpenTradeCount:    a.OpenTradeCount,
		OpenPositionCount: a.OpenPositionCount,
	}, nil
}

// GetPositions returns all open positions (with non-zero units).
func (c *Client) GetPositions(ctx context.Context, creds exchange.Credentials) ([]PositionInfo, error) {
	path := fmt.Sprintf("/v3/accounts/%s/openPositions", creds.AccountID)
	var resp struct {
		Positions []struct {
			Instrument string `json:"instrument"`
			Long       struct {
				Units        string `json:"units"`
				AveragePrice string `json:"averagePrice"`
				PL           string `json:"pl"`
				UnrealizedPL string `json:"unrealizedPL"`
			} `json:"long"`
			Short struct {
				Units        string `json:"units"`
				AveragePrice string `json:"averagePrice"`
				PL           string `json:"pl"`
				UnrealizedPL string `json:"unrealizedPL"`
			} `json:"short"`
			UnrealizedPL string `json:"unrealizedPL"`
		} `json:"positions"`
	}

	if err := c.doRequest(ctx, creds, "GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("oanda positions: %w", err)
	}

	results := make([]PositionInfo, len(resp.Positions))
	for i, p := range resp.Positions {
		results[i] = PositionInfo{
			Instrument:    p.Instrument,
			LongUnits:     parseFloat(p.Long.Units),
			LongAvgPrice:  parseFloat(p.Long.AveragePrice),
			LongPL:        parseFloat(p.Long.PL),
			ShortUnits:    math.Abs(parseFloat(p.Short.Units)),
			ShortAvgPrice: parseFloat(p.Short.AveragePrice),
			ShortPL:       parseFloat(p.Short.PL),
			UnrealizedPL:  parseFloat(p.UnrealizedPL),
		}
	}
	return results, nil
}

// ClosePosition closes an entire position for an instrument.
func (c *Client) ClosePosition(ctx context.Context, creds exchange.Credentials, instrument string) error {
	path := fmt.Sprintf("/v3/accounts/%s/positions/%s/close", creds.AccountID, instrument)
	body := map[string]string{"longUnits": "ALL", "shortUnits": "ALL"}
	var resp interface{}
	if err := c.doRequest(ctx, creds, "PUT", path, body, &resp); err != nil {
		return fmt.Errorf("oanda close position %s: %w", instrument, err)
	}
	return nil
}

// GetInstruments returns the list of tradable instruments for the account.
func (c *Client) GetInstruments(ctx context.Context, creds exchange.Credentials) ([]InstrumentInfo, error) {
	path := fmt.Sprintf("/v3/accounts/%s/instruments", creds.AccountID)
	var resp struct {
		Instruments []struct {
			Name                string `json:"name"`
			DisplayName         string `json:"displayName"`
			Type                string `json:"type"`
			PipLocation         int    `json:"pipLocation"`
			TradeUnitsPrecision int    `json:"tradeUnitsPrecision"`
			MinimumTradeSize    string `json:"minimumTradeSize"`
		} `json:"instruments"`
	}

	if err := c.doRequest(ctx, creds, "GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("oanda instruments: %w", err)
	}

	results := make([]InstrumentInfo, len(resp.Instruments))
	for i, inst := range resp.Instruments {
		results[i] = InstrumentInfo{
			Name:                inst.Name,
			DisplayName:         inst.DisplayName,
			Type:                inst.Type,
			PipLocation:         inst.PipLocation,
			TradeUnitsPrecision: inst.TradeUnitsPrecision,
			MinTradeSize:        inst.MinimumTradeSize,
		}
	}
	return results, nil
}
