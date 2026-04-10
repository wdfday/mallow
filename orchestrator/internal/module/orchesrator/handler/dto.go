package handler

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	botdomain "orchestrator/internal/module/bot/domain"
	"orchestrator/internal/module/orchesrator/domain"
	"orchestrator/internal/runtime/core/portfolio"
)

// ── Request DTOs ───────────────────────────────────────────────────────────

type CreateOrchestratorReq struct {
	AccountID uuid.UUID         `json:"account_id" binding:"required"`
	Name      string            `json:"name" binding:"required,min=1,max=128"`
	Capital   float64           `json:"capital" binding:"required,gt=0"`
	Exchange  ExchangeConfigDTO `json:"exchange" binding:"required"`
	Risk      RiskConfigDTO     `json:"risk"`
}

type UpdateOrchestratorReq struct {
	Name    string         `json:"name" binding:"omitempty,min=1,max=128"`
	Capital float64        `json:"capital" binding:"omitempty,gt=0"`
	Risk    *RiskConfigDTO `json:"risk"`
}

type ExchangeConfigDTO struct {
	BrokerType string `json:"broker_type" binding:"required,oneof=alpaca binance okx bybit ibkr oanda"`
	APIKey     string `json:"api_key,omitempty"`
	APISecret  string `json:"api_secret,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	AccountID  string `json:"account_id,omitempty"`
	BaseURL    string `json:"base_url,omitempty"`
	StreamURL  string `json:"stream_url,omitempty"`
	Demo       bool   `json:"demo,omitempty"`
	Testnet    bool   `json:"testnet,omitempty"`
}

type RiskConfigDTO struct {
	MaxPositions      int     `json:"max_positions" binding:"omitempty,min=1"`
	MaxPositionPct    float64 `json:"max_position_pct" binding:"omitempty,gt=0,lte=1"`
	DailyLossLimitPct float64 `json:"daily_loss_limit_pct" binding:"omitempty,gt=0,lte=1"`
	MaxDrawdownPct    float64 `json:"max_drawdown_pct" binding:"omitempty,gt=0,lte=1"`
}

// ── Response DTOs ──────────────────────────────────────────────────────────

type OrchestratorResp struct {
	ID        uuid.UUID          `json:"id"`
	AccountID uuid.UUID          `json:"account_id"`
	Name      string             `json:"name"`
	Capital   decimal.Decimal    `json:"capital"`
	Exchange  ExchangeConfigResp `json:"exchange"`
	Risk      RiskConfigDTO      `json:"risk"`
	Enabled   bool               `json:"enabled"`
	Status    string             `json:"status"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

type ExchangeConfigResp struct {
	BrokerType string `json:"broker_type"`
	BaseURL    string `json:"base_url,omitempty"`
	Demo       bool   `json:"demo,omitempty"`
	Testnet    bool   `json:"testnet,omitempty"`
}

type BotTacticResp struct {
	Entry       string         `json:"entry,omitempty"`
	Exit        string         `json:"exit,omitempty"`
	Name        string         `json:"name,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
	MinStrength float64        `json:"min_strength,omitempty"`
}

type BotRiskResp struct {
	SizeMode          string          `json:"size_mode,omitempty"`
	RiskPerTradePct   float64         `json:"risk_per_trade_pct,omitempty"`
	MaxPositionPct    float64         `json:"max_position_pct,omitempty"`
	FixedQty          decimal.Decimal `json:"fixed_qty,omitempty"`
	StopLossATRMult   float64         `json:"stop_loss_atr_mult,omitempty"`
	TakeProfitATRMult float64         `json:"take_profit_atr_mult,omitempty"`
	StopLossPct       float64         `json:"stop_loss_pct,omitempty"`
	TakeProfitPct     float64         `json:"take_profit_pct,omitempty"`
	TrailingStopPct   float64         `json:"trailing_stop_pct,omitempty"`
	MaxBarsHeld       int             `json:"max_bars_held,omitempty"`
}

type BotHealthResp struct {
	Status       string `json:"status"`
	StartedAt    string `json:"started_at,omitempty"`
	LastSignalAt string `json:"last_signal_at,omitempty"`
	LastOrderAt  string `json:"last_order_at,omitempty"`
	LastErrorAt  string `json:"last_error_at,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	Uptime       string `json:"uptime,omitempty"`
}

type BotMetricsResp struct {
	SignalsReceived int64           `json:"signals_received"`
	SignalsFiltered int64           `json:"signals_filtered"`
	TradesApproved  int64           `json:"trades_approved"`
	OrdersPlaced    int64           `json:"orders_placed"`
	OrdersFilled    int64           `json:"orders_filled"`
	OrdersFailed    int64           `json:"orders_failed"`
	TotalPnL        decimal.Decimal `json:"total_pnl"`
	WinCount        int64           `json:"win_count"`
	LossCount       int64           `json:"loss_count"`
}

type BotSummaryResp struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Type           string         `json:"type"`
	OrchestratorID uuid.UUID      `json:"orchestrator_id"`
	Tactic         BotTacticResp  `json:"tactic"`
	Risk           BotRiskResp    `json:"risk"`
	Symbols        []string       `json:"symbols"`
	Status         string         `json:"status"`
	Running        bool           `json:"running"`
	OrderCount     int            `json:"order_count"`
	Health         BotHealthResp  `json:"health"`
	Metrics        BotMetricsResp `json:"metrics"`
	CreatedAt      time.Time      `json:"created_at"`
}

type OrchestratorDetailResp struct {
	OrchestratorResp
	Bots       []BotSummaryResp `json:"bots"`
	Running    bool             `json:"running"`
	Paused     bool             `json:"paused"`
	LastSyncAt *time.Time       `json:"last_sync_at,omitempty"`
}

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

type ActionResp struct {
	Status string    `json:"status"`
	ID     uuid.UUID `json:"id"`
}

// ── Conversions ────────────────────────────────────────────────────────────

func (d ExchangeConfigDTO) ToDomain() domain.ExchangeConfig {
	return domain.ExchangeConfig{
		BrokerType: d.BrokerType,
		APIKey:     d.APIKey,
		APISecret:  d.APISecret,
		Passphrase: d.Passphrase,
		AccountID:  d.AccountID,
		BaseURL:    d.BaseURL,
		StreamURL:  d.StreamURL,
		Demo:       d.Demo,
		Testnet:    d.Testnet,
	}
}

func (d RiskConfigDTO) ToDomain() domain.RiskConfig {
	return domain.RiskConfig{
		MaxPositions:      d.MaxPositions,
		MaxPositionPct:    d.MaxPositionPct,
		DailyLossLimitPct: d.DailyLossLimitPct,
		MaxDrawdownPct:    d.MaxDrawdownPct,
	}
}

func riskToDTO(r domain.RiskConfig) RiskConfigDTO {
	return RiskConfigDTO{
		MaxPositions:      r.MaxPositions,
		MaxPositionPct:    r.MaxPositionPct,
		DailyLossLimitPct: r.DailyLossLimitPct,
		MaxDrawdownPct:    r.MaxDrawdownPct,
	}
}

func orchToResp(cfg *domain.OrchestratorConfig) OrchestratorResp {
	return OrchestratorResp{
		ID:        cfg.ID,
		AccountID: cfg.AccountID,
		Name:      cfg.Name,
		Capital:   decimal.NewFromFloat(cfg.Capital),
		Exchange: ExchangeConfigResp{
			BrokerType: cfg.Exchange.BrokerType,
			BaseURL:    cfg.Exchange.BaseURL,
			Demo:       cfg.Exchange.Demo,
			Testnet:    cfg.Exchange.Testnet,
		},
		Risk:      riskToDTO(cfg.Risk),
		Enabled:   cfg.Enabled,
		Status:    cfg.Status,
		CreatedAt: cfg.CreatedAt,
		UpdatedAt: cfg.UpdatedAt,
	}
}

func orchListToResp(cfgs []*domain.OrchestratorConfig) []OrchestratorResp {
	out := make([]OrchestratorResp, len(cfgs))
	for i, cfg := range cfgs {
		out[i] = orchToResp(cfg)
	}
	return out
}

func BotSummaryToResp(b botdomain.BotSummary) BotSummaryResp {
	return BotSummaryResp{
		ID:             b.ID,
		Name:           b.Name,
		Type:           string(b.Type),
		OrchestratorID: b.OrchestratorID,
		Tactic: BotTacticResp{
			Entry:       b.Strategy.Entry,
			Exit:        b.Strategy.Exit,
			Name:        b.Strategy.Name,
			Params:      b.Strategy.Params,
			MinStrength: b.Strategy.MinStrength,
		},
		Risk: BotRiskResp{
			SizeMode:          b.Risk.SizeMode,
			RiskPerTradePct:   b.Risk.RiskPerTradePct,
			MaxPositionPct:    b.Risk.MaxPositionPct,
			FixedQty:          b.Risk.FixedQty,
			StopLossATRMult:   b.Risk.StopLossATRMult,
			TakeProfitATRMult: b.Risk.TakeProfitATRMult,
			StopLossPct:       b.Risk.StopLossPct,
			TakeProfitPct:     b.Risk.TakeProfitPct,
			TrailingStopPct:   b.Risk.TrailingStopPct,
			MaxBarsHeld:       b.Risk.MaxBarsHeld,
		},
		Symbols:    b.Symbols,
		Status:     b.Status,
		Running:    b.Running,
		OrderCount: b.OrderCount,
		Health: BotHealthResp{
			Status:       b.Health.Status,
			StartedAt:    b.Health.StartedAt,
			LastSignalAt: b.Health.LastSignalAt,
			LastOrderAt:  b.Health.LastOrderAt,
			LastErrorAt:  b.Health.LastErrorAt,
			LastError:    b.Health.LastError,
			Uptime:       b.Health.Uptime,
		},
		Metrics: BotMetricsResp{
			SignalsReceived: b.Metrics.SignalsReceived,
			SignalsFiltered: b.Metrics.SignalsFiltered,
			TradesApproved:  b.Metrics.TradesApproved,
			OrdersPlaced:    b.Metrics.OrdersPlaced,
			OrdersFilled:    b.Metrics.OrdersFilled,
			OrdersFailed:    b.Metrics.OrdersFailed,
			TotalPnL:        b.Metrics.TotalPnL,
			WinCount:        b.Metrics.WinCount,
			LossCount:       b.Metrics.LossCount,
		},
		CreatedAt: b.CreatedAt,
	}
}

func BotSummariesToResp(items []botdomain.BotSummary) []BotSummaryResp {
	out := make([]BotSummaryResp, len(items))
	for i, item := range items {
		out[i] = BotSummaryToResp(item)
	}
	return out
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

type OrderResp struct {
	ID             string          `json:"id"`
	BotID          string          `json:"bot_id"`
	OrchestratorID string          `json:"orchestrator_id"`
	Symbol         string          `json:"symbol"`
	Side           string          `json:"side"`
	Qty            decimal.Decimal `json:"qty"`
	Type           string          `json:"type"`
	Status         string          `json:"status"`
	FilledQty      decimal.Decimal `json:"filled_qty"`
	FilledAvg      decimal.Decimal `json:"filled_avg_price"`
	SubmitTime     time.Time       `json:"submitted_at"`
}

func OrderToResp(o botdomain.Order) OrderResp {
	return OrderResp{
		ID:             o.ID,
		BotID:          o.BotID,
		OrchestratorID: o.OrchestratorID,
		Symbol:         o.Symbol,
		Side:           o.Side,
		Qty:            o.Qty,
		Type:           o.Type,
		Status:         o.Status,
		FilledQty:      o.FilledQty,
		FilledAvg:      o.FilledAvg,
		SubmitTime:     o.SubmitTime,
	}
}
