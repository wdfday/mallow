package binance

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"
	"mallow/helm/internal/infra/exchange/client"

	binancesdk "github.com/adshao/go-binance/v2"
)

const demoBinanceURL = "https://demo-api.binance.com"
const demoFuturesURL = "https://demo-fapi.binance.com"
const demoCoinMURL = "https://demo-dapi.binance.com"

// Client implements broker.BrokerClient for Binance.
type Client struct{}

func NewClient() *Client { return &Client{} }

func newSDK(creds client.Credentials) *binancesdk.Client {
	c := binancesdk.NewClient(creds.APIKey, creds.APISecret)
	if creds.IsPaper {
		c.BaseURL = demoBinanceURL
	}
	return c
}

func (c *Client) Validate(_ context.Context, creds client.Credentials) error {
	if _, err := newSDK(creds).NewGetAccountService().Do(context.Background()); err != nil {
		return fmt.Errorf("binance authentication failed: %w", err)
	}
	return nil
}

func (c *Client) GetPortfolio(ctx context.Context, creds client.Credentials) (*client.Portfolio, error) {
	account, err := newSDK(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance get account failed: %w", err)
	}
	var total, cash decimal.Decimal
	for _, b := range account.Balances {
		free, _ := decimal.NewFromString(b.Free)
		locked, _ := decimal.NewFromString(b.Locked)
		qty := free.Add(locked)
		total = total.Add(qty)
		if b.Asset == "USDT" || b.Asset == "USDC" || b.Asset == "BUSD" {
			cash = cash.Add(qty)
		}
	}
	return &client.Portfolio{TotalValue: total, Currency: "USDT", CashBalance: cash}, nil
}

var binanceStablecoins = map[string]bool{
	"USDT": true, "USDC": true, "BUSD": true, "DAI": true, "TUSD": true, "FDUSD": true,
}

func (c *Client) GetPositions(ctx context.Context, creds client.Credentials) ([]client.Position, error) {
	switch creds.AccountType {
	case "futures_usdm":
		return c.getFuturesPositions(ctx, creds)
	case "futures_coinm":
		return c.getCoinMPositions(ctx, creds)
	}
	return c.getSpotPositions(ctx, creds)
}

func (c *Client) getSpotPositions(ctx context.Context, creds client.Credentials) ([]client.Position, error) {
	sdk := newSDK(creds)
	account, err := sdk.NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance get positions failed: %w", err)
	}

	type holding struct {
		asset string
		qty   decimal.Decimal
	}
	var holdings []holding
	for _, b := range account.Balances {
		if binanceStablecoins[b.Asset] {
			continue
		}
		free, _ := decimal.NewFromString(b.Free)
		locked, _ := decimal.NewFromString(b.Locked)
		qty := free.Add(locked)
		if !qty.IsPositive() {
			continue
		}
		holdings = append(holdings, holding{asset: b.Asset, qty: qty})
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

	positions := make([]client.Position, 0, len(holdings))
	for _, h := range holdings {
		instID := h.asset + "USDT"
		curPrice := priceMap[instID]
		positions = append(positions, client.Position{
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

func (c *Client) getFuturesPositions(ctx context.Context, creds client.Credentials) ([]client.Position, error) {
	sdk := binancesdk.NewFuturesClient(creds.APIKey, creds.APISecret)
	if creds.IsPaper {
		sdk.BaseURL = demoFuturesURL
	}
	risk, err := sdk.NewGetPositionRiskService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance futures positions failed: %w", err)
	}
	var positions []client.Position
	for _, p := range risk {
		qty, _ := decimal.NewFromString(p.PositionAmt)
		if qty.IsZero() {
			continue
		}
		avgPx, _ := decimal.NewFromString(p.EntryPrice)
		markPx, _ := decimal.NewFromString(p.MarkPrice)
		upl, _ := decimal.NewFromString(p.UnRealizedProfit)
		positions = append(positions, client.Position{
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

func (c *Client) getCoinMPositions(ctx context.Context, creds client.Credentials) ([]client.Position, error) {
	sdk := binancesdk.NewDeliveryClient(creds.APIKey, creds.APISecret)
	if creds.IsPaper {
		sdk.BaseURL = demoCoinMURL
	}
	risk, err := sdk.NewGetPositionRiskService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance coin-m positions failed: %w", err)
	}
	var positions []client.Position
	for _, p := range risk {
		qty, _ := decimal.NewFromString(p.PositionAmt)
		if qty.IsZero() {
			continue
		}
		avgPx, _ := decimal.NewFromString(p.EntryPrice)
		markPx, _ := decimal.NewFromString(p.MarkPrice)
		upl, _ := decimal.NewFromString(p.UnRealizedProfit)
		positions = append(positions, client.Position{
			Symbol:             p.Symbol,
			Name:               p.Symbol,
			AssetType:          "crypto",
			Quantity:           qty.Abs(),
			AverageCostPerUnit: avgPx,
			CurrentPrice:       markPx,
			CurrentValue:       qty.Abs().Mul(markPx),
			UnrealizedGain:     upl,
			Currency:           "USD",
			Exchange:           "BINANCE",
			ExternalID:         p.Symbol,
			LastUpdated:        time.Now(),
		})
	}
	return positions, nil
}

// DetectAccounts implements client.MultiAccountDetector.
// Probes spot, USDT-margined futures, and coin-margined futures in parallel.
// Returns an entry for each sub-account that has a non-zero balance.
func (c *Client) DetectAccounts(ctx context.Context, creds client.Credentials) ([]client.DetectedAccount, error) {
	type result struct {
		acct client.DetectedAccount
		err  error
	}
	ch := make(chan result, 3)

	// Spot
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panic recovered", "recover", r)
				ch <- result{err: fmt.Errorf("panic: %v", r)}
			}
		}()
		sdk := newSDK(creds)
		acct, err := sdk.NewGetAccountService().Do(ctx)
		if err != nil {
			ch <- result{err: err}
			return
		}
		var cash decimal.Decimal
		for _, b := range acct.Balances {
			if !binanceStablecoins[b.Asset] {
				continue
			}
			free, _ := decimal.NewFromString(b.Free)
			cash = cash.Add(free)
		}
		ch <- result{acct: client.DetectedAccount{AccountType: "spot", CashBalance: cash}}
	}()

	// Futures USDM
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panic recovered", "recover", r)
				ch <- result{err: fmt.Errorf("panic: %v", r)}
			}
		}()
		sdk := binancesdk.NewFuturesClient(creds.APIKey, creds.APISecret)
		if creds.IsPaper {
			sdk.BaseURL = demoFuturesURL
		}
		balances, err := sdk.NewGetBalanceService().Do(ctx)
		if err != nil {
			ch <- result{} // not available — skip silently
			return
		}
		var cash decimal.Decimal
		for _, b := range balances {
			if b.Asset != "USDT" && b.Asset != "USDC" && b.Asset != "BUSD" {
				continue
			}
			v, _ := decimal.NewFromString(b.Balance)
			cash = cash.Add(v)
		}
		if cash.IsPositive() {
			ch <- result{acct: client.DetectedAccount{AccountType: "futures_usdm", CashBalance: cash}}
		} else {
			ch <- result{}
		}
	}()

	// Futures CoinM
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panic recovered", "recover", r)
				ch <- result{err: fmt.Errorf("panic: %v", r)}
			}
		}()
		sdk := binancesdk.NewDeliveryClient(creds.APIKey, creds.APISecret)
		if creds.IsPaper {
			sdk.BaseURL = demoCoinMURL
		}
		balances, err := sdk.NewGetBalanceService().Do(ctx)
		if err != nil {
			ch <- result{}
			return
		}
		var cash decimal.Decimal
		for _, b := range balances {
			v, _ := decimal.NewFromString(b.Balance)
			cash = cash.Add(v)
		}
		if cash.IsPositive() {
			ch <- result{acct: client.DetectedAccount{AccountType: "futures_coinm", CashBalance: cash}}
		} else {
			ch <- result{}
		}
	}()

	var accounts []client.DetectedAccount
	for range 3 {
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
