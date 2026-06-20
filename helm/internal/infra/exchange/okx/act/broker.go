package act

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	brokerclient "mallow/helm/internal/infra/exchange/client"
)

// Broker wraps Client to implement brokerclient.BrokerClient.
// Using a separate type avoids method-name collisions with Client's own GetPositions.
type Broker struct {
	c *Client
}

// NewBroker returns a BrokerClient backed by the shared act.Client.
func NewBroker(c *Client) *Broker { return &Broker{c: c} }

var _ brokerclient.BrokerClient = (*Broker)(nil)

// toOKXCreds converts broker-layer Credentials to exchange.Credentials.
func toOKXCreds(cc brokerclient.Credentials) exchange.Credentials {
	var passphrase string
	if cc.Passphrase != nil {
		passphrase = *cc.Passphrase
	}
	return exchange.Credentials{
		APIKey:     cc.APIKey,
		APISecret:  cc.APISecret,
		Passphrase: passphrase,
	}
}

func (b *Broker) Validate(ctx context.Context, cc brokerclient.Credentials) error {
	if cc.Passphrase == nil || *cc.Passphrase == "" {
		return fmt.Errorf("passphrase is required for OKX")
	}
	var out struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := b.c.doRequestAs(ctx, toOKXCreds(cc), cc.IsPaper, http.MethodGet, "/api/v5/account/balance", nil, &out); err != nil {
		return fmt.Errorf("okx validate: %w", err)
	}
	if out.Code != "0" {
		return fmt.Errorf("okx validate: %s", out.Msg)
	}
	return nil
}

func (b *Broker) GetPortfolio(ctx context.Context, cc brokerclient.Credentials) (*brokerclient.Portfolio, error) {
	if cc.Passphrase == nil || *cc.Passphrase == "" {
		return nil, fmt.Errorf("passphrase is required for OKX")
	}
	var out struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			TotalEq string `json:"totalEq"`
			Details []struct {
				Ccy     string `json:"ccy"`
				EqUsd   string `json:"eqUsd"`
				CashBal string `json:"cashBal"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := b.c.doRequestAs(ctx, toOKXCreds(cc), cc.IsPaper, http.MethodGet, "/api/v5/account/balance", nil, &out); err != nil {
		return nil, fmt.Errorf("okx portfolio: %w", err)
	}
	if out.Code != "0" {
		return nil, fmt.Errorf("okx portfolio: %s", out.Msg)
	}
	if len(out.Data) == 0 {
		return &brokerclient.Portfolio{Currency: "USD", LastUpdated: time.Now()}, nil
	}
	data := out.Data[0]
	totalValue, _ := decimal.NewFromString(data.TotalEq)
	assetAllocation := make(map[string]decimal.Decimal)
	var cashBalance decimal.Decimal
	for _, detail := range data.Details {
		value, _ := decimal.NewFromString(detail.EqUsd)
		if !value.IsPositive() {
			continue
		}
		assetAllocation[detail.Ccy] = value
		if detail.Ccy == "USDT" || detail.Ccy == "USD" || detail.Ccy == "USDC" {
			cashBalance = cashBalance.Add(value)
		}
	}
	return &brokerclient.Portfolio{
		TotalValue:      totalValue,
		CashBalance:     cashBalance,
		Currency:        "USD",
		AssetAllocation: assetAllocation,
		LastUpdated:     time.Now(),
	}, nil
}

func (b *Broker) GetPositions(ctx context.Context, cc brokerclient.Credentials) ([]brokerclient.Position, error) {
	if cc.Passphrase == nil || *cc.Passphrase == "" {
		return nil, fmt.Errorf("passphrase is required for OKX")
	}
	creds := toOKXCreds(cc)
	spot, err := b.okxSpotPositions(ctx, creds, cc.IsPaper)
	if err != nil {
		return nil, err
	}
	deriv, err := b.okxDerivPositions(ctx, creds, cc.IsPaper)
	if err != nil {
		return nil, err
	}
	return append(spot, deriv...), nil
}

func (b *Broker) okxSpotPositions(ctx context.Context, creds exchange.Credentials, paper bool) ([]brokerclient.Position, error) {
	var out struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			Details []struct {
				Ccy      string `json:"ccy"`
				AvailBal string `json:"availBal"`
				Eq       string `json:"eq"`
				EqUsd    string `json:"eqUsd"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := b.c.doRequestAs(ctx, creds, paper, http.MethodGet, "/api/v5/account/balance", nil, &out); err != nil {
		return nil, fmt.Errorf("okx spot positions: %w", err)
	}
	if out.Code != "0" || len(out.Data) == 0 {
		return nil, nil
	}
	stablecoins := map[string]bool{"USDT": true, "USDC": true, "USD": true, "DAI": true, "BUSD": true}
	var positions []brokerclient.Position
	for _, d := range out.Data[0].Details {
		if stablecoins[d.Ccy] {
			continue
		}
		qty, _ := decimal.NewFromString(d.Eq)
		if !qty.IsPositive() {
			continue
		}
		valueUSD, _ := decimal.NewFromString(d.EqUsd)
		var curPrice decimal.Decimal
		if qty.IsPositive() {
			curPrice = valueUSD.Div(qty)
		}
		instID := d.Ccy + "-USDT"
		positions = append(positions, brokerclient.Position{
			Symbol:       instID,
			Name:         instID,
			AssetType:    "crypto",
			Quantity:     qty,
			CurrentPrice: curPrice,
			CurrentValue: valueUSD,
			Currency:     "USD",
			Exchange:     "OKX",
			ExternalID:   instID,
			LastUpdated:  time.Now(),
		})
	}
	return positions, nil
}

func (b *Broker) okxDerivPositions(ctx context.Context, creds exchange.Credentials, paper bool) ([]brokerclient.Position, error) {
	var out struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstId      string `json:"instId"`
			Pos         string `json:"pos"`
			AvgPx       string `json:"avgPx"`
			MarkPx      string `json:"markPx"`
			Upl         string `json:"upl"`
			UplRatio    string `json:"uplRatio"`
			NotionalUsd string `json:"notionalUsd"`
		} `json:"data"`
	}
	if err := b.c.doRequestAs(ctx, creds, paper, http.MethodGet, "/api/v5/account/positions", nil, &out); err != nil {
		return nil, fmt.Errorf("okx deriv positions: %w", err)
	}
	if out.Code != "0" {
		return nil, fmt.Errorf("okx deriv positions: %s", out.Msg)
	}
	positions := make([]brokerclient.Position, 0, len(out.Data))
	for _, pos := range out.Data {
		qty, _ := decimal.NewFromString(pos.Pos)
		if qty.IsZero() {
			continue
		}
		uplRatio, _ := decimal.NewFromString(pos.UplRatio)
		avgPx, _ := decimal.NewFromString(pos.AvgPx)
		markPx, _ := decimal.NewFromString(pos.MarkPx)
		notional, _ := decimal.NewFromString(pos.NotionalUsd)
		upl, _ := decimal.NewFromString(pos.Upl)
		positions = append(positions, brokerclient.Position{
			Symbol:             pos.InstId,
			Name:               pos.InstId,
			AssetType:          "crypto",
			Quantity:           qty,
			AverageCostPerUnit: avgPx,
			CurrentPrice:       markPx,
			CurrentValue:       notional,
			UnrealizedGain:     upl,
			UnrealizedGainPct:  uplRatio.Mul(decimal.NewFromInt(100)).InexactFloat64(),
			Currency:           "USD",
			Exchange:           "OKX",
			ExternalID:         pos.InstId,
			LastUpdated:        time.Now(),
		})
	}
	return positions, nil
}
