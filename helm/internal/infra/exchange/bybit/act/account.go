package act

import (
	"context"
	"fmt"

	"mallow/helm/internal/infra/exchange"
)

// WalletBalance represents the balance for a single coin.
type WalletBalance struct {
	Coin                string
	Free                float64
	Locked              float64
	WalletBalance       float64
	UnrealizedPL        float64
	CumRealizedPL       float64
	AvailableToWithdraw float64
}

// AccountInfo is a simplified view of a Bybit account wallet.
type AccountInfo struct {
	AccountType  string
	TotalEquity  float64
	TotalBalance float64
	TotalPnL     float64
	Coins        []WalletBalance
}

// FeeRate represents trading fee rates for a symbol.
type FeeRate struct {
	Symbol       string
	MakerFeeRate float64
	TakerFeeRate float64
}

// GetWalletBalance returns the wallet balance for the given account type (UNIFIED, SPOT, CONTRACT).
func (c *Client) GetWalletBalance(ctx context.Context, creds exchange.Credentials, accountType string) (*AccountInfo, error) {
	body := map[string]string{"accountType": accountType}

	var resp apiResponse[walletBalanceResult]
	if err := c.doSigned(ctx, creds, "GET", "/v5/account/wallet-balance", body, &resp); err != nil {
		return nil, fmt.Errorf("bybit wallet balance: %w", err)
	}
	if resp.RetCode != 0 {
		return nil, bybitErr("wallet balance", resp.RetCode, resp.RetMsg)
	}
	if len(resp.Result.List) == 0 {
		return nil, fmt.Errorf("bybit: no wallet data")
	}

	acct := resp.Result.List[0]
	coins := make([]WalletBalance, 0, len(acct.Coin))
	for _, coin := range acct.Coin {
		if wb := parseFloat(coin.WalletBalance); wb > 0 {
			// UNIFIED accounts use availableBalance for trading; SPOT uses availableToWithdraw.
			free := parseFloat(coin.AvailableBalance)
			if free == 0 {
				free = parseFloat(coin.AvailableToWithdraw)
			}
			coins = append(coins, WalletBalance{
				Coin:                coin.Coin,
				Free:                free,
				Locked:              parseFloat(coin.Locked),
				WalletBalance:       wb,
				UnrealizedPL:        parseFloat(coin.UnrealizedPnl),
				CumRealizedPL:       parseFloat(coin.CumRealisedPnl),
				AvailableToWithdraw: parseFloat(coin.AvailableToWithdraw),
			})
		}
	}

	return &AccountInfo{
		AccountType:  acct.AccountType,
		TotalEquity:  parseFloat(acct.TotalEquity),
		TotalBalance: parseFloat(acct.TotalWalletBalance),
		TotalPnL:     parseFloat(acct.TotalPnl),
		Coins:        coins,
	}, nil
}

// GetFeeRate returns the trading fee rate for a category/symbol.
func (c *Client) GetFeeRate(ctx context.Context, creds exchange.Credentials, category, symbol string) (*FeeRate, error) {
	body := map[string]string{"category": category}
	if symbol != "" {
		body["symbol"] = symbol
	}

	var resp apiResponse[feeRateResult]
	if err := c.doSigned(ctx, creds, "GET", "/v5/account/fee-rate", body, &resp); err != nil {
		return nil, fmt.Errorf("bybit fee rate: %w", err)
	}
	if resp.RetCode != 0 || len(resp.Result.List) == 0 {
		return nil, bybitErr("fee rate", resp.RetCode, resp.RetMsg)
	}

	f := resp.Result.List[0]
	return &FeeRate{
		Symbol:       f.Symbol,
		MakerFeeRate: parseFloat(f.MakerFeeRate),
		TakerFeeRate: parseFloat(f.TakerFeeRate),
	}, nil
}
