package act

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"
	"github.com/shopspring/decimal"

	brokerclient "mallow/helm/internal/infra/exchange/client"
)

const demoFuturesURL = "https://demo-fapi.binance.com"

// Broker wraps Client to implement brokerclient.BrokerClient.
type Broker struct {
	c *Client
}

// NewBroker returns a BrokerClient backed by the shared act.Client.
func NewBroker(c *Client) *Broker { return &Broker{c: c} }

var _ brokerclient.BrokerClient = (*Broker)(nil)
var _ brokerclient.MultiAccountDetector = (*Broker)(nil)

// newSpotBroker creates a gobinance.Client honouring cc.IsPaper, reusing the shared HTTP pool.
func (b *Broker) newSpotBroker(cc brokerclient.Credentials) *gobinance.Client {
	cl := gobinance.NewClient(cc.APIKey, cc.APISecret)
	cl.HTTPClient = b.c.httpClient
	cl.TimeOffset = b.c.timeOffset.Load()
	if cc.IsPaper {
		cl.BaseURL = paperBaseURL
	}
	return cl
}

func (b *Broker) newFuturesBroker(cc brokerclient.Credentials) *futures.Client {
	cl := gobinance.NewFuturesClient(cc.APIKey, cc.APISecret)
	if cc.IsPaper {
		cl.BaseURL = demoFuturesURL
	}
	return cl
}

func (b *Broker) Validate(_ context.Context, cc brokerclient.Credentials) error {
	if _, err := b.newSpotBroker(cc).NewGetAccountService().Do(context.Background()); err != nil {
		return fmt.Errorf("binance validate: %w", err)
	}
	return nil
}

func (b *Broker) GetPortfolio(ctx context.Context, cc brokerclient.Credentials) (*brokerclient.Portfolio, error) {
	account, err := b.newSpotBroker(cc).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance portfolio: %w", err)
	}
	var total, cash decimal.Decimal
	for _, bal := range account.Balances {
		free, _ := decimal.NewFromString(bal.Free)
		locked, _ := decimal.NewFromString(bal.Locked)
		qty := free.Add(locked)
		total = total.Add(qty)
		if bal.Asset == "USDT" || bal.Asset == "USDC" || bal.Asset == "BUSD" {
			cash = cash.Add(qty)
		}
	}
	return &brokerclient.Portfolio{TotalValue: total, Currency: "USDT", CashBalance: cash, LastUpdated: time.Now()}, nil
}

var binanceBrokerStablecoins = map[string]bool{
	"USDT": true, "USDC": true, "BUSD": true, "DAI": true, "TUSD": true, "FDUSD": true,
}

func (b *Broker) GetPositions(ctx context.Context, cc brokerclient.Credentials) ([]brokerclient.Position, error) {
	if cc.AccountType == "futures_usdm" {
		return b.getFuturesPositions(ctx, cc)
	}
	return b.getSpotPositions(ctx, cc)
}

func (b *Broker) getSpotPositions(ctx context.Context, cc brokerclient.Credentials) ([]brokerclient.Position, error) {
	sdk := b.newSpotBroker(cc)
	account, err := sdk.NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance spot positions: %w", err)
	}
	type holding struct {
		asset string
		qty   decimal.Decimal
	}
	var holdings []holding
	for _, bal := range account.Balances {
		if binanceBrokerStablecoins[bal.Asset] {
			continue
		}
		free, _ := decimal.NewFromString(bal.Free)
		locked, _ := decimal.NewFromString(bal.Locked)
		qty := free.Add(locked)
		if !qty.IsPositive() {
			continue
		}
		holdings = append(holdings, holding{asset: bal.Asset, qty: qty})
	}
	if len(holdings) == 0 {
		return nil, nil
	}
	allPrices, _ := sdk.NewListPricesService().Do(ctx)
	priceMap := make(map[string]decimal.Decimal, len(allPrices))
	for _, p := range allPrices {
		v, _ := decimal.NewFromString(p.Price)
		priceMap[p.Symbol] = v
	}
	positions := make([]brokerclient.Position, 0, len(holdings))
	for _, h := range holdings {
		instID := h.asset + "USDT"
		curPrice := priceMap[instID]
		positions = append(positions, brokerclient.Position{
			Symbol:       instID,
			Name:         instID,
			AssetType:    "crypto",
			Quantity:     h.qty,
			CurrentPrice: curPrice,
			CurrentValue: h.qty.Mul(curPrice),
			Currency:     "USDT",
			Exchange:     "BINANCE",
			ExternalID:   instID,
			LastUpdated:  time.Now(),
		})
	}
	return positions, nil
}

func (b *Broker) getFuturesPositions(ctx context.Context, cc brokerclient.Credentials) ([]brokerclient.Position, error) {
	risk, err := b.newFuturesBroker(cc).NewGetPositionRiskService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance futures positions: %w", err)
	}
	var positions []brokerclient.Position
	for _, p := range risk {
		qty, _ := decimal.NewFromString(p.PositionAmt)
		if qty.IsZero() {
			continue
		}
		avgPx, _ := decimal.NewFromString(p.EntryPrice)
		markPx, _ := decimal.NewFromString(p.MarkPrice)
		upl, _ := decimal.NewFromString(p.UnRealizedProfit)
		positions = append(positions, brokerclient.Position{
			Symbol:             p.Symbol,
			Name:               p.Symbol,
			AssetType:          "crypto",
			Quantity:           qty.Abs(),
			AverageCostPerUnit: avgPx,
			CurrentPrice:       markPx,
			CurrentValue:       qty.Abs().Mul(markPx),
			UnrealizedGain:     upl,
			Currency:           "USDT",
			Exchange:           "BINANCE",
			ExternalID:         p.Symbol,
			LastUpdated:        time.Now(),
		})
	}
	return positions, nil
}

// DetectAccounts implements brokerclient.MultiAccountDetector.
func (b *Broker) DetectAccounts(ctx context.Context, cc brokerclient.Credentials) ([]brokerclient.DetectedAccount, error) {
	type result struct {
		acct brokerclient.DetectedAccount
		err  error
	}
	ch := make(chan result, 2)

	// Spot
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("binance broker: spot detect panic", "recover", r)
				ch <- result{err: fmt.Errorf("panic: %v", r)}
			}
		}()
		acct, err := b.newSpotBroker(cc).NewGetAccountService().Do(ctx)
		if err != nil {
			ch <- result{err: err}
			return
		}
		var cash decimal.Decimal
		for _, bal := range acct.Balances {
			if !binanceBrokerStablecoins[bal.Asset] {
				continue
			}
			free, _ := decimal.NewFromString(bal.Free)
			cash = cash.Add(free)
		}
		ch <- result{acct: brokerclient.DetectedAccount{AccountType: "spot", CashBalance: cash}}
	}()

	// Futures USDM
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("binance broker: futures detect panic", "recover", r)
				ch <- result{} // not available
			}
		}()
		balances, err := b.newFuturesBroker(cc).NewGetBalanceService().Do(ctx)
		if err != nil {
			ch <- result{}
			return
		}
		var cash decimal.Decimal
		for _, bal := range balances {
			if bal.Asset != "USDT" && bal.Asset != "USDC" && bal.Asset != "BUSD" {
				continue
			}
			v, _ := decimal.NewFromString(bal.Balance)
			cash = cash.Add(v)
		}
		if cash.IsPositive() {
			ch <- result{acct: brokerclient.DetectedAccount{AccountType: "futures_usdm", CashBalance: cash}}
		} else {
			ch <- result{}
		}
	}()

	var accounts []brokerclient.DetectedAccount
	for range 2 {
		r := <-ch
		if r.err != nil {
			return nil, r.err
		}
		if r.acct.AccountType != "" {
			accounts = append(accounts, r.acct)
		}
	}
	return accounts, nil
}
