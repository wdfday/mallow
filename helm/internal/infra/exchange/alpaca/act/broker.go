package act

import (
	"context"
	"fmt"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/shopspring/decimal"

	brokerclient "mallow/helm/internal/infra/exchange/client"
)

const (
	alpacaLiveURL  = "https://api.alpaca.markets"
	alpacaPaperURL = "https://paper-api.alpaca.markets"
)

// Broker wraps Client to implement brokerclient.BrokerClient.
type Broker struct {
	c *Client
}

// NewBroker returns a BrokerClient backed by the shared act.Client.
func NewBroker(c *Client) *Broker { return &Broker{c: c} }

var _ brokerclient.BrokerClient = (*Broker)(nil)

func (b *Broker) newSDKBroker(cc brokerclient.Credentials) *alpacasdk.Client {
	baseURL := alpacaLiveURL
	if cc.IsPaper {
		baseURL = alpacaPaperURL
	}
	return alpacasdk.NewClient(alpacasdk.ClientOpts{
		APIKey:    cc.APIKey,
		APISecret: cc.APISecret,
		BaseURL:   baseURL,
	})
}

func (b *Broker) Validate(_ context.Context, cc brokerclient.Credentials) error {
	if _, err := b.newSDKBroker(cc).GetAccount(); err != nil {
		return fmt.Errorf("alpaca validate: %w", err)
	}
	return nil
}

func (b *Broker) GetPortfolio(_ context.Context, cc brokerclient.Credentials) (*brokerclient.Portfolio, error) {
	account, err := b.newSDKBroker(cc).GetAccount()
	if err != nil {
		return nil, fmt.Errorf("alpaca portfolio: %w", err)
	}
	return &brokerclient.Portfolio{TotalValue: account.Equity, Currency: "USD", CashBalance: account.Cash}, nil
}

func (b *Broker) GetPositions(_ context.Context, cc brokerclient.Credentials) ([]brokerclient.Position, error) {
	positions, err := b.newSDKBroker(cc).GetPositions()
	if err != nil {
		return nil, fmt.Errorf("alpaca positions: %w", err)
	}
	result := make([]brokerclient.Position, 0, len(positions))
	for _, p := range positions {
		curPrice := decimal.Zero
		if p.CurrentPrice != nil {
			curPrice = *p.CurrentPrice
		}
		result = append(result, brokerclient.Position{
			Symbol:       string(p.Symbol),
			Quantity:     p.Qty,
			CurrentPrice: curPrice,
			Currency:     "USD",
		})
	}
	return result, nil
}
