package act

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// BalanceInfo represents a single currency balance on OKX.
type BalanceInfo struct {
	Currency    string
	Available   float64
	Frozen      float64
	CashBalance float64
	Equity      float64
}

// AccountInfo is a simplified view of the OKX account.
type AccountInfo struct {
	TotalEquity float64
	Balances    []BalanceInfo
}

// InstrumentInfo represents a tradable instrument on OKX.
type InstrumentInfo struct {
	InstID   string
	InstType string // SPOT, MARGIN, SWAP, FUTURES, OPTION
	BaseCcy  string
	QuoteCcy string
	State    string
	TickSz   string
	LotSz    string
	MinSz    string
}

// PositionInfo represents an open position on OKX.
type PositionInfo struct {
	InstID  string
	PosSide string // long, short, net
	Pos     float64
	AvgPx   float64
	Upl     float64
	Lever   float64
	LiqPx   float64
	MarkPx  float64
}

// GetBalance returns the unified account balance.
func (c *Client) GetBalance(ctx context.Context, creds exchange.Credentials) (*AccountInfo, error) {
	var resp struct {
		okxEnvelope
		Data []struct {
			TotalEq string `json:"totalEq"`
			Details []struct {
				Ccy       string `json:"ccy"`
				AvailBal  string `json:"availBal"`
				FrozenBal string `json:"frozenBal"`
				CashBal   string `json:"cashBal"`
				Eq        string `json:"eq"`
			} `json:"details"`
		} `json:"data"`
	}

	if err := c.doRequest(ctx, creds, http.MethodGet, "/api/v5/account/balance", nil, &resp); err != nil {
		return nil, fmt.Errorf("okx get balance: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return nil, fmt.Errorf("okx balance: code=%s msg=%s", resp.Code, resp.Msg)
	}

	d := resp.Data[0]
	balances := make([]BalanceInfo, 0, len(d.Details))
	for _, det := range d.Details {
		if eq := parseFloat(det.Eq); eq > 0 {
			balances = append(balances, BalanceInfo{
				Currency:    det.Ccy,
				Available:   parseFloat(det.AvailBal),
				Frozen:      parseFloat(det.FrozenBal),
				CashBalance: parseFloat(det.CashBal),
				Equity:      eq,
			})
		}
	}

	return &AccountInfo{TotalEquity: parseFloat(d.TotalEq), Balances: balances}, nil
}

// GetPositions returns all open positions for an optional instType (SWAP, FUTURES, etc).
func (c *Client) GetPositions(ctx context.Context, creds exchange.Credentials, instType string) ([]PositionInfo, error) {
	path := "/api/v5/account/positions"
	if instType != "" {
		path += "?instType=" + instType
	}

	var resp struct {
		okxEnvelope
		Data []struct {
			InstID  string `json:"instId"`
			PosSide string `json:"posSide"`
			Pos     string `json:"pos"`
			AvgPx   string `json:"avgPx"`
			Upl     string `json:"upl"`
			Lever   string `json:"lever"`
			LiqPx   string `json:"liqPx"`
			MarkPx  string `json:"markPx"`
		} `json:"data"`
	}

	if err := c.doRequest(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("okx get positions: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx positions: code=%s msg=%s", resp.Code, resp.Msg)
	}

	results := make([]PositionInfo, len(resp.Data))
	for i, p := range resp.Data {
		results[i] = PositionInfo{
			InstID:  p.InstID,
			PosSide: p.PosSide,
			Pos:     parseFloat(p.Pos),
			AvgPx:   parseFloat(p.AvgPx),
			Upl:     parseFloat(p.Upl),
			Lever:   parseFloat(p.Lever),
			LiqPx:   parseFloat(p.LiqPx),
			MarkPx:  parseFloat(p.MarkPx),
		}
	}
	return results, nil
}

// GetInstrument returns info about a specific trading instrument (public endpoint).
func (c *Client) GetInstrument(ctx context.Context, instType, instID string) (*InstrumentInfo, error) {
	path := fmt.Sprintf("/api/v5/public/instruments?instType=%s&instId=%s", instType, instID)

	var resp struct {
		okxEnvelope
		Data []struct {
			InstID   string `json:"instId"`
			InstType string `json:"instType"`
			BaseCcy  string `json:"baseCcy"`
			QuoteCcy string `json:"quoteCcy"`
			State    string `json:"state"`
			TickSz   string `json:"tickSz"`
			LotSz    string `json:"lotSz"`
			MinSz    string `json:"minSz"`
		} `json:"data"`
	}

	if err := c.doRequest(ctx, exchange.Credentials{}, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("okx get instrument: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return nil, fmt.Errorf("okx instrument: code=%s msg=%s", resp.Code, resp.Msg)
	}

	d := resp.Data[0]
	return &InstrumentInfo{
		InstID:   d.InstID,
		InstType: d.InstType,
		BaseCcy:  d.BaseCcy,
		QuoteCcy: d.QuoteCcy,
		State:    d.State,
		TickSz:   d.TickSz,
		LotSz:    d.LotSz,
		MinSz:    d.MinSz,
	}, nil
}

// GetSymbolFilters implements exchange.SymbolInfoProvider.
func (c *Client) GetSymbolFilters(ctx context.Context, symbol string) (exchange.SymbolFilters, error) {
	instType := "SPOT"
	if strings.HasSuffix(symbol, "-SWAP") {
		instType = "SWAP"
	}
	info, err := c.GetInstrument(ctx, instType, symbol)
	if err != nil {
		return exchange.SymbolFilters{}, err
	}
	f := exchange.SymbolFilters{
		BaseAsset:  info.BaseCcy,
		QuoteAsset: info.QuoteCcy,
	}
	f.QtyStep, _ = decimal.NewFromString(info.LotSz)
	f.MinQty, _ = decimal.NewFromString(info.MinSz)
	f.PriceTick, _ = decimal.NewFromString(info.TickSz)
	return f, nil
}
