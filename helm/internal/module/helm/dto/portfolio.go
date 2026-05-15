package dto

import (
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/core/portfolio"
)

// ── Config DTOs ────────────────────────────────────────────────────────────

// PortfolioConfigDTO is the API shape for account-level capital allocation settings.
type PortfolioConfigDTO struct {
	MaxPositions   int     `json:"max_positions,omitempty" binding:"omitempty,min=1"`
	MaxPositionPct float64 `json:"max_position_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	ReserveRatio   float64 `json:"reserve_ratio,omitempty" binding:"omitempty,gte=0,lt=1"`
}

// RiskConfigDTO is the API shape for account-level risk circuit-breakers.
type RiskConfigDTO struct {
	DailyLossLimitPct float64 `json:"daily_loss_limit_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	MaxDrawdownPct    float64 `json:"max_drawdown_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
}

// ── Response DTOs ──────────────────────────────────────────────────────────

type PortfolioResp struct {
	InitialCapital decimal.Decimal `json:"initial_capital"`
	Cash           decimal.Decimal `json:"cash"`
	Equity         decimal.Decimal `json:"equity"`
	TotalReturn    float64         `json:"total_return_pct"`
	CurrentDD      float64         `json:"current_drawdown_pct"`
	MaxDD          float64         `json:"max_drawdown_pct"`
	WinRate        float64         `json:"win_rate_pct"`
	TotalTrades    int             `json:"total_trades"`
	OpenPositions  int             `json:"open_positions"`
	DailyPnL       decimal.Decimal `json:"daily_pnl"`
	Positions      []PositionResp  `json:"positions"`
}

type PositionResp struct {
	Symbol        string          `json:"symbol"`
	Qty           decimal.Decimal `json:"qty"`
	AvgPrice      decimal.Decimal `json:"avg_price"`
	CurrentPrice  decimal.Decimal `json:"current_price"`
	UnrealizedPnL decimal.Decimal `json:"unrealized_pnl"`
	MarketValue   decimal.Decimal `json:"market_value"`
	EntryTime     time.Time       `json:"entry_time"`
}

type TradeResp struct {
	Symbol     string          `json:"symbol"`
	Side       string          `json:"side"`
	Qty        decimal.Decimal `json:"qty"`
	EntryPrice decimal.Decimal `json:"entry_price"`
	ExitPrice  decimal.Decimal `json:"exit_price"`
	EntryTime  time.Time       `json:"entry_time"`
	ExitTime   time.Time       `json:"exit_time"`
	PnL        decimal.Decimal `json:"pnl"`
	PnLPct     decimal.Decimal `json:"pnl_pct"`
}

// ── Conversions ────────────────────────────────────────────────────────────

func (d PortfolioConfigDTO) ToDomain() domain.PortfolioConfig {
	return domain.PortfolioConfig{
		MaxPositions:   d.MaxPositions,
		MaxPositionPct: d.MaxPositionPct,
		ReserveRatio:   d.ReserveRatio,
	}
}

func portfolioToDTO(p domain.PortfolioConfig) PortfolioConfigDTO {
	return PortfolioConfigDTO{
		MaxPositions:   p.MaxPositions,
		MaxPositionPct: p.MaxPositionPct,
		ReserveRatio:   p.ReserveRatio,
	}
}

func (d RiskConfigDTO) ToDomain() domain.RiskConfig {
	return domain.RiskConfig{
		DailyLossLimitPct: d.DailyLossLimitPct,
		MaxDrawdownPct:    d.MaxDrawdownPct,
	}
}

func riskToDTO(r domain.RiskConfig) RiskConfigDTO {
	return RiskConfigDTO{
		DailyLossLimitPct: r.DailyLossLimitPct,
		MaxDrawdownPct:    r.MaxDrawdownPct,
	}
}

func PortfolioToResp(s portfolio.Summary) PortfolioResp {
	positions := make([]PositionResp, len(s.Positions))
	for i, p := range s.Positions {
		positions[i] = PositionResp{
			Symbol:        p.Symbol,
			Qty:           p.Qty,
			AvgPrice:      p.AvgPrice,
			CurrentPrice:  p.CurrentPrice,
			UnrealizedPnL: p.UnrealizedPnL,
			MarketValue:   p.MarketValue,
			EntryTime:     p.EntryTimestamp,
		}
	}
	return PortfolioResp{
		InitialCapital: s.InitialCapital,
		Cash:           s.Cash,
		Equity:         s.Equity,
		TotalReturn:    s.TotalReturn,
		CurrentDD:      s.CurrentDD,
		MaxDD:          s.MaxDD,
		WinRate:        s.WinRate,
		TotalTrades:    s.TotalTrades,
		OpenPositions:  s.OpenPositions,
		DailyPnL:       s.DailyPnL,
		Positions:      positions,
	}
}

func TradesToResp(trades []portfolio.Trade) []TradeResp {
	out := make([]TradeResp, len(trades))
	for i, t := range trades {
		out[i] = TradeResp{
			Symbol:     t.Symbol,
			Side:       string(t.Side),
			Qty:        t.Qty,
			EntryPrice: t.EntryPrice,
			ExitPrice:  t.ExitPrice,
			EntryTime:  t.EntryTimestamp,
			ExitTime:   t.ExitTimestamp,
			PnL:        t.PnL,
			PnLPct:     t.PnLPct,
		}
	}
	return out
}

func PositionsToResp(positions []portfolio.Position) []PositionResp {
	out := make([]PositionResp, len(positions))
	for i, p := range positions {
		out[i] = PositionResp{
			Symbol:        p.Symbol,
			Qty:           p.Qty,
			AvgPrice:      p.AvgPrice,
			CurrentPrice:  p.CurrentPrice,
			UnrealizedPnL: p.UnrealizedPnL,
			MarketValue:   p.MarketValue,
			EntryTime:     p.EntryTimestamp,
		}
	}
	return out
}
