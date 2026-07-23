package act

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	brokerclient "mallow/helm/internal/infra/exchange/client"
)

// Broker wraps Client to implement brokerclient.BrokerClient.
type Broker struct {
	c *Client
}

// NewBroker returns a BrokerClient backed by the shared act.Client.
func NewBroker(c *Client) *Broker { return &Broker{c: c} }

var _ brokerclient.BrokerClient = (*Broker)(nil)

func bybitURL(paper bool) string {
	if paper {
		return "https://api-demo.bybit.com"
	}
	return "https://api.bybit.com"
}

func toBybitCreds(cc brokerclient.Credentials) exchange.Credentials {
	return exchange.Credentials{
		APIKey:    cc.APIKey,
		APISecret: cc.APISecret,
	}
}

func (b *Broker) Validate(ctx context.Context, cc brokerclient.Credentials) error {
	var out struct {
		RetCode int `json:"retCode"`
	}
	if err := b.c.doSignedAt(ctx, toBybitCreds(cc), bybitURL(cc.IsPaper), http.MethodGet, "/v5/user/query-api", nil, &out); err != nil {
		return fmt.Errorf("bybit validate: %w", err)
	}
	if out.RetCode != 0 {
		return bybitErr("validate", out.RetCode, "")
	}
	return nil
}

// GetExternalUID returns Bybit's own user id from the API-key-info endpoint
// (the same endpoint Validate already calls).
func (b *Broker) GetExternalUID(ctx context.Context, cc brokerclient.Credentials) (string, error) {
	var out struct {
		RetCode int `json:"retCode"`
		Result  struct {
			UserID int64 `json:"userID"`
		} `json:"result"`
	}
	if err := b.c.doSignedAt(ctx, toBybitCreds(cc), bybitURL(cc.IsPaper), http.MethodGet, "/v5/user/query-api", nil, &out); err != nil {
		return "", fmt.Errorf("bybit get external uid: %w", err)
	}
	if out.RetCode != 0 {
		return "", bybitErr("get external uid", out.RetCode, "")
	}
	return strconv.FormatInt(out.Result.UserID, 10), nil
}

func (b *Broker) GetPortfolio(ctx context.Context, cc brokerclient.Credentials) (*brokerclient.Portfolio, error) {
	var out struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				TotalEquity string `json:"totalEquity"`
			} `json:"list"`
		} `json:"result"`
	}
	body := map[string]string{"accountType": "UNIFIED"}
	if err := b.c.doSignedAt(ctx, toBybitCreds(cc), bybitURL(cc.IsPaper), http.MethodGet, "/v5/account/wallet-balance", body, &out); err != nil {
		return nil, fmt.Errorf("bybit portfolio: %w", err)
	}
	var total decimal.Decimal
	if len(out.Result.List) > 0 {
		total, _ = decimal.NewFromString(out.Result.List[0].TotalEquity)
	}
	return &brokerclient.Portfolio{TotalValue: total, Currency: "USDT", LastUpdated: time.Now()}, nil
}

func (b *Broker) GetPositions(ctx context.Context, cc brokerclient.Credentials) ([]brokerclient.Position, error) {
	var out struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				Symbol        string `json:"symbol"`
				Size          string `json:"size"`
				AvgPrice      string `json:"avgPrice"`
				MarkPrice     string `json:"markPrice"`
				UnrealisedPnl string `json:"unrealisedPnl"`
			} `json:"list"`
		} `json:"result"`
	}
	body := map[string]string{"category": "linear", "limit": "200"}
	if err := b.c.doSignedAt(ctx, toBybitCreds(cc), bybitURL(cc.IsPaper), http.MethodGet, "/v5/position/list", body, &out); err != nil {
		return nil, fmt.Errorf("bybit positions: %w", err)
	}
	var positions []brokerclient.Position
	for _, p := range out.Result.List {
		qty, _ := decimal.NewFromString(p.Size)
		if !qty.IsPositive() {
			continue
		}
		avgPx, _ := decimal.NewFromString(p.AvgPrice)
		markPx, _ := decimal.NewFromString(p.MarkPrice)
		pnl, _ := decimal.NewFromString(p.UnrealisedPnl)
		positions = append(positions, brokerclient.Position{
			Symbol:             p.Symbol,
			Quantity:           qty,
			AverageCostPerUnit: avgPx,
			CurrentPrice:       markPx,
			UnrealizedGain:     pnl,
			Currency:           "USDT",
			LastUpdated:        time.Now(),
		})
	}
	return positions, nil
}
