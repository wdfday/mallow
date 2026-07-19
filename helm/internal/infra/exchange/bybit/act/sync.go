package act

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

var _ exchange.AccountSyncer = (*Client)(nil)

// SyncAccount implements exchange.AccountSyncer. Bybit's account model is
// unified: one wallet-balance call covers spot holdings, and a separate
// position-list call covers linear (perpetual) positions — same two-part
// shape ListPositions (reconcile.go) already uses, but built directly here
// so the richer per-position fields (leverage, mark price, liquidation
// price, margin mode) reach AccountSnapshot instead of being narrowed away
// through the PositionResult DTO that ListPositions returns to its own
// (different) caller.
//
// Assumes accountType=UNIFIED, matching ListPositions' existing assumption
// for this codebase — Bybit's classic (non-unified) SPOT+CONTRACT split
// isn't handled here; add accountType detection if that's ever needed.
func (c *Client) SyncAccount(ctx context.Context, creds exchange.Credentials, _ *time.Time) (*exchange.AccountSnapshot, error) {
	info, err := c.GetWalletBalance(ctx, creds, "UNIFIED")
	if err != nil {
		return nil, fmt.Errorf("bybit sync: get wallet balance: %w", err)
	}

	cash := decimal.Zero
	balances := make([]exchange.AssetBalance, 0, len(info.Coins))
	spotPositions := make([]exchange.ExchangePosition, 0, len(info.Coins))
	for _, coin := range info.Coins {
		free := decimal.NewFromFloat(coin.Free)
		locked := decimal.NewFromFloat(coin.Locked)
		wallet := decimal.NewFromFloat(coin.WalletBalance)
		balances = append(balances, exchange.AssetBalance{
			Asset:         coin.Coin,
			Free:          free,
			Locked:        locked,
			MarginBalance: wallet,
			UnrealizedPnL: decimal.NewFromFloat(coin.UnrealizedPL),
		})
		if coin.Coin == "USDT" {
			cash = cash.Add(free)
			continue
		}
		if bybitStablecoins[coin.Coin] || !wallet.IsPositive() {
			continue
		}
		// Non-stablecoin spot holding — treat as a position, same as ListPositions.
		spotPositions = append(spotPositions, exchange.ExchangePosition{
			Symbol: coin.Coin + "USDT",
			Qty:    wallet,
		})
	}

	var posResp apiResponse[positionListResult]
	body := map[string]string{"category": "linear", "settleCoin": "USDT"}
	if err := c.doSigned(ctx, creds, "GET", "/v5/position/list", body, &posResp); err != nil {
		slog.Warn("bybit sync: linear positions fetch failed — spot holdings only", "err", err)
		return buildBybitSnapshot(cash, info, spotPositions, balances), nil
	}
	if posResp.RetCode != 0 {
		slog.Warn("bybit sync: linear positions bad response", "code", posResp.RetCode, "msg", posResp.RetMsg)
		return buildBybitSnapshot(cash, info, spotPositions, balances), nil
	}

	positions := spotPositions
	for _, p := range posResp.Result.List {
		size := parseDecimal(p.Size)
		if size.IsZero() {
			continue
		}
		marginMode := "cross"
		if p.TradeMode == 1 {
			marginMode = "isolated"
		}
		positions = append(positions, exchange.ExchangePosition{
			Symbol:           p.Symbol,
			Qty:              size,
			AvgPrice:         parseDecimal(p.AvgPrice),
			CurPrice:         parseDecimal(p.MarkPrice),
			Side:             bybitPositionSide(p.Side),
			UnrealizedPnL:    parseDecimal(p.UnrealisedPnl),
			Leverage:         parseDecimal(p.Leverage),
			LiquidationPrice: parseDecimal(p.LiqPrice),
			MarginMode:       marginMode,
		})
	}

	return buildBybitSnapshot(cash, info, positions, balances), nil
}

func buildBybitSnapshot(cash decimal.Decimal, info *AccountInfo, positions []exchange.ExchangePosition, balances []exchange.AssetBalance) *exchange.AccountSnapshot {
	mv := decimal.Zero
	for _, p := range positions {
		mv = mv.Add(p.Qty.Mul(p.CurPrice))
	}
	return &exchange.AccountSnapshot{
		Cash:      cash,
		Equity:    cash.Add(mv),
		Positions: positions,
		Balances:  balances,
		// AccountEquity/AccountMode are already parsed into AccountInfo — Bybit's
		// own margin-adjusted total and the wallet's UNIFIED/CONTRACT/SPOT mode.
		AccountEquity: decimal.NewFromFloat(info.TotalEquity),
		AccountMode:   strings.ToLower(info.AccountType),
	}
}

// bybitPositionSide maps Bybit's "Buy"/"Sell"/"None" position side to
// exchange.PositionSide's "long"/"short"/"net" convention.
func bybitPositionSide(side string) exchange.PositionSide {
	switch side {
	case "Buy":
		return exchange.PositionLong
	case "Sell":
		return exchange.PositionShort
	default:
		return exchange.PositionNet
	}
}
