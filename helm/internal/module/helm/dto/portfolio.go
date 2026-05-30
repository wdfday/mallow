package dto

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/infra/poslog"
	analyticsdomain "mallow/helm/internal/module/analytics/domain"
	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/readmodel"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/perf"
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

// PortfolioResp is the helm-level financial snapshot returned by GET /helms/:id/portfolio.
// See docs/metrics-and-reports.md §1–§2 for field definitions.
type PortfolioResp struct {
	InitialCapital decimal.Decimal `json:"initial_capital"`

	// Capital fields (broker-account truth).
	Cash            decimal.Decimal `json:"cash"`             // free quote balance
	Equity          decimal.Decimal `json:"equity"`           // cash + Σ MtM positions
	DeployedCapital decimal.Decimal `json:"deployed_capital"` // entry-cost basis of open positions
	UnrealizedPnL   decimal.Decimal `json:"unrealized_pnl"`   // Σ open-position MtM delta
	RealizedPnL     decimal.Decimal `json:"realized_pnl"`     // Σ closed-trade PnL since hydrate

	// Hand-allocation aggregates (cross-hand).
	AllocatedToHands   decimal.Decimal `json:"allocated_to_hands"`  // Σ hand.AllocatedCapital
	UnallocatedCapital decimal.Decimal `json:"unallocated_capital"` // Cash − AllocatedToHands; "free to assign to a new hand"

	// Deprecated alias of Cash, kept for backward-compat. FE should prefer Cash.
	AvailableCash decimal.Decimal `json:"available_cash"`

	TotalReturn   float64         `json:"total_return_pct"`
	CurrentDD     float64         `json:"current_drawdown_pct"`
	MaxDD         float64         `json:"max_drawdown_pct"`
	WinRate       float64         `json:"win_rate_pct"`
	TotalTrades   int             `json:"total_trades"`
	OpenPositions int             `json:"open_positions"`
	DailyPnL      decimal.Decimal `json:"daily_pnl"`
	Positions     []PositionResp  `json:"positions"`
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

// TradeResp is the analytical view of one closed round-trip trade.
// Field set follows docs/metrics-and-reports.md §4. All risk-adjusted KPIs
// (R-multiple, planned risk) and attribution columns (pattern, timeframe,
// regime) are first-class. Decimal fields use decimal.Decimal preserving
// full precision; consumers compute pnl_pct on the fly if needed.
type TradeResp struct {
	ID        string          `json:"id,omitempty"` // PG row id (empty if reading from poslog)
	HelmID    string          `json:"helm_id,omitempty"`
	HandID    string          `json:"hand_id,omitempty"`
	Symbol    string          `json:"symbol"`
	Side      string          `json:"side"` // entry side: buy/sell
	Qty       decimal.Decimal `json:"qty"`
	Timeframe string          `json:"timeframe,omitempty"` // M1/M5/H1/...

	EntryPrice decimal.Decimal `json:"entry_price"`
	ExitPrice  decimal.Decimal `json:"exit_price"`
	EntryTime  time.Time       `json:"entry_time"`
	ExitTime   time.Time       `json:"exit_time"`

	GrossPnL   decimal.Decimal `json:"gross_pnl"`  // (exit-entry)*qty, no fees
	Commission decimal.Decimal `json:"commission"` // total entry+exit fees
	NetPnL     decimal.Decimal `json:"net_pnl"`    // gross_pnl − commission
	PnLPct     decimal.Decimal `json:"pnl_pct"`    // net_pnl / (entry_price × qty), computed

	StopLossPrice   decimal.Decimal `json:"stop_loss_price,omitzero"`
	TakeProfitPrice decimal.Decimal `json:"take_profit_price,omitzero"`
	PlannedRisk     decimal.Decimal `json:"planned_risk,omitzero"`
	RMultiple       decimal.Decimal `json:"r_multiple,omitzero"` // net_pnl / planned_risk

	EntryOrderID   string `json:"entry_order_id,omitempty"`
	ExitOrderID    string `json:"exit_order_id,omitempty"`
	NEntries       int    `json:"n_entries,omitempty"`       // 1 for non-pyramid; 1+adds for pyramid
	HoldingSeconds int    `json:"holding_seconds,omitempty"` // exit - entry

	ExitReason  string `json:"exit_reason,omitempty"` // signal/sl/tp/kill/bracket_exit/external/release
	Strategy    string `json:"strategy,omitempty"`
	RegimeState string `json:"regime_state,omitempty"`
}

// TradesPageResp is the paged response for trade history.
// When backed by PostgreSQL: `Next` carries an RFC3339 exit_at cursor (exclusive).
// When backed by JetStream poslog: `Next` carries a JetStream sequence number.
type TradesPageResp struct {
	Trades   []TradeResp       `json:"trades"`
	Next     string            `json:"next,omitempty"` // unified string cursor; empty = no more
	HasMore  bool              `json:"has_more"`
	Limit    int               `json:"limit"`
	Metadata AnalyticsMetaResp `json:"metadata,omitempty"`
}

// AnalyticsMetaResp surfaces the analytics service's response metadata to FE.
// See docs/metrics-and-reports.md for `persister_lag_s` semantics.
type AnalyticsMetaResp struct {
	Source        string    `json:"source"`
	GeneratedAt   time.Time `json:"generated_at"`
	PersisterLagS float64   `json:"persister_lag_s"`
}

// StatsResp is the FE-facing KPI bundle. Field set mirrors analytics/domain.Stats
// 1:1; ratios are float64 for ergonomic chart rendering.
// See docs/metrics-and-reports.md §9 for the canonical KPI definitions.
type StatsResp struct {
	NTrades int     `json:"n_trades"`
	WinRate float64 `json:"win_rate"`

	GrossProfit  decimal.Decimal `json:"gross_profit"`
	GrossLoss    decimal.Decimal `json:"gross_loss"`
	NetPnL       decimal.Decimal `json:"net_pnl"`
	Commission   decimal.Decimal `json:"commission"`
	ProfitFactor float64         `json:"profit_factor"`

	AvgWin      decimal.Decimal `json:"avg_win"`
	AvgLoss     decimal.Decimal `json:"avg_loss"`
	Expectancy  decimal.Decimal `json:"expectancy"`
	ExpectancyR float64         `json:"expectancy_r"`

	LargestWin  decimal.Decimal `json:"largest_win"`
	LargestLoss decimal.Decimal `json:"largest_loss"`

	AvgHoldingSeconds int `json:"avg_holding_seconds"`

	BySymbol []GroupedKPIResp `json:"by_symbol,omitempty"`
	ByExit   []GroupedKPIResp `json:"by_exit,omitempty"`

	Metadata AnalyticsMetaResp `json:"metadata,omitempty"`
}

// GroupedKPIResp is one row in an attribution slice (by_symbol / by_pattern / by_exit).
type GroupedKPIResp struct {
	Key     string          `json:"key"`
	NTrades int             `json:"n_trades"`
	WinRate float64         `json:"win_rate"`
	NetPnL  decimal.Decimal `json:"net_pnl"`
	AvgR    float64         `json:"avg_r"`
}

// StatsToResp converts a service result into the FE response.
func StatsToResp(s analyticsdomain.Stats, m analyticsdomain.Metadata) StatsResp {
	r := StatsResp{
		NTrades:           s.NTrades,
		WinRate:           s.WinRate,
		GrossProfit:       s.GrossProfit,
		GrossLoss:         s.GrossLoss,
		NetPnL:            s.NetPnL,
		Commission:        s.Commission,
		ProfitFactor:      s.ProfitFactor,
		AvgWin:            s.AvgWin,
		AvgLoss:           s.AvgLoss,
		Expectancy:        s.Expectancy,
		ExpectancyR:       s.ExpectancyR,
		LargestWin:        s.LargestWin,
		LargestLoss:       s.LargestLoss,
		AvgHoldingSeconds: s.AvgHoldingSeconds,
		Metadata:          MetadataResp(m),
	}
	convert := func(in []analyticsdomain.GroupedKPI) []GroupedKPIResp {
		out := make([]GroupedKPIResp, len(in))
		for i, g := range in {
			out[i] = GroupedKPIResp{Key: g.Key, NTrades: g.NTrades, WinRate: g.WinRate, NetPnL: g.NetPnL, AvgR: g.AvgR}
		}
		return out
	}
	r.BySymbol = convert(s.BySymbol)
	r.ByExit = convert(s.ByExit)
	return r
}

// MetadataResp converts an analytics.domain.Metadata into the response wrapper.
func MetadataResp(m analyticsdomain.Metadata) AnalyticsMetaResp {
	return AnalyticsMetaResp{
		Source:        m.Source,
		GeneratedAt:   m.GeneratedAt,
		PersisterLagS: m.PersisterLagS,
	}
}

// TradeRecordToResp converts a poslog TradeRecord to TradeResp.
// Lacks PG-only fields (commission, planned_risk, r_multiple, …) — those are
// available only via tradelog.TradeRecord. Use TradelogToResp when reading
// from PostgreSQL.
func TradeRecordToResp(t poslog.TradeRecord) TradeResp {
	qty, _ := decimal.NewFromString(t.Qty)
	entry, _ := decimal.NewFromString(t.EntryPrice)
	exit, _ := decimal.NewFromString(t.ExitPrice)
	pnl, _ := decimal.NewFromString(t.RealizedPnL)
	var pnlPct decimal.Decimal
	cost := entry.Mul(qty)
	if cost.IsPositive() {
		pnlPct = pnl.Div(cost)
	}
	holding := 0
	if !t.EntryAt.IsZero() {
		holding = int(t.ExitAt.Sub(t.EntryAt).Seconds())
	}
	return TradeResp{
		HandID:         t.HandID,
		Symbol:         t.Symbol,
		Side:           t.Side,
		Qty:            qty,
		EntryPrice:     entry,
		ExitPrice:      exit,
		EntryTime:      t.EntryAt,
		ExitTime:       t.ExitAt,
		GrossPnL:       pnl,
		NetPnL:         pnl, // commission unknown in poslog payload — net == gross here
		PnLPct:         pnlPct,
		ExitReason:     t.Source,
		HoldingSeconds: holding,
	}
}

// PoslogPageToResp converts a poslog.TradesPage to the API response type.
// Cursor is encoded as a base-10 string of the JetStream sequence.
func PoslogPageToResp(p poslog.TradesPage, limit int) TradesPageResp {
	out := TradesPageResp{
		Trades:  make([]TradeResp, 0, len(p.Trades)),
		HasMore: p.HasMore,
		Limit:   limit,
	}
	if p.HasMore {
		out.Next = strconv.FormatUint(p.Next, 10)
	}
	for _, t := range p.Trades {
		out.Trades = append(out.Trades, TradeRecordToResp(t))
	}
	return out
}

// TradelogToResp converts a PG-backed tradelog.TradeRecord to TradeResp,
// including all analytical columns (commission, planned_risk, r_multiple, …).
func TradelogToResp(t readmodel.TradeRecord) TradeResp {
	var pnlPct decimal.Decimal
	cost := t.EntryPrice.Mul(t.Qty)
	net := t.PnL.Sub(t.Commission)
	if cost.IsPositive() {
		pnlPct = net.Div(cost)
	}
	holding := 0
	if !t.EntryAt.IsZero() {
		holding = int(t.ExitAt.Sub(t.EntryAt).Seconds())
	}
	return TradeResp{
		ID:              t.ID.String(),
		HelmID:          t.HelmID.String(),
		HandID:          t.HandID.String(),
		Symbol:          t.Symbol,
		Side:            t.Side,
		Qty:             t.Qty,
		Timeframe:       t.Timeframe,
		EntryPrice:      t.EntryPrice,
		ExitPrice:       t.ExitPrice,
		EntryTime:       t.EntryAt,
		ExitTime:        t.ExitAt,
		GrossPnL:        t.PnL,
		Commission:      t.Commission,
		NetPnL:          net,
		PnLPct:          pnlPct,
		StopLossPrice:   t.StopLossPrice,
		TakeProfitPrice: t.TakeProfitPrice,
		PlannedRisk:     t.PlannedRisk,
		RMultiple:       t.RMultiple,
		EntryOrderID:    t.EntryOrderID,
		ExitOrderID:     t.ExitOrderID,
		NEntries:        t.NEntries,
		HoldingSeconds:  holding,
		ExitReason:      t.Source,
		Strategy:        t.Strategy,
		RegimeState:     t.RegimeState,
	}
}

// ── Fills ──────────────────────────────────────────────────────────────────

type FillResp struct {
	TradeID  string          `json:"trade_id"`
	OrderID  string          `json:"order_id"`
	HandID   string          `json:"hand_id,omitempty"`
	Kind     string          `json:"kind"`
	Symbol   string          `json:"symbol"`
	Side     string          `json:"side"`
	Qty      decimal.Decimal `json:"qty"`
	AvgPrice decimal.Decimal `json:"avg_price"`
	Fee      decimal.Decimal `json:"fee"`
	FilledAt time.Time       `json:"filled_at"`
}

type FillPageResp struct {
	Fills   []FillResp `json:"fills"`
	Next    string     `json:"next,omitempty"` // RFC3339 cursor; empty = end
	HasMore bool       `json:"has_more"`
	Limit   int        `json:"limit"`
}

func FillToResp(t natsapi.TransactionMsg) FillResp {
	return FillResp{
		TradeID:  t.TradeID,
		OrderID:  t.OrderID,
		HandID:   t.HandID,
		Kind:     t.Kind,
		Symbol:   t.Symbol,
		Side:     t.Side,
		Qty:      t.Qty,
		AvgPrice: t.AvgPrice,
		Fee:      t.Fee,
		FilledAt: t.FilledAt,
	}
}

// ── Snapshots ──────────────────────────────────────────────────────────────

type SnapshotPositionResp struct {
	Symbol   string          `json:"symbol"`
	Side     string          `json:"side"`
	Qty      decimal.Decimal `json:"qty"`
	AvgPrice decimal.Decimal `json:"avg_price"`
}

// SnapshotResp is one raw equity_snapshots row. Reflects the full PG schema
// (event-driven, no bucketing) — for time-aligned chart points use EquityPointResp.
type SnapshotResp struct {
	HelmID        string                 `json:"helm_id"`
	HandID        string                 `json:"hand_id,omitempty"`
	TS            time.Time              `json:"ts"`
	Cash          decimal.Decimal        `json:"cash,omitempty"`
	Equity        decimal.Decimal        `json:"equity,omitempty"`
	RealizedPnL   decimal.Decimal        `json:"realized_pnl,omitempty"`
	UnrealizedPnL decimal.Decimal        `json:"unrealized_pnl,omitempty"`
	Positions     []SnapshotPositionResp `json:"positions"`
}

type SnapshotPageResp struct {
	Snapshots []SnapshotResp    `json:"snapshots"`
	Next      string            `json:"next,omitempty"`
	HasMore   bool              `json:"has_more"`
	Limit     int               `json:"limit"`
	Metadata  AnalyticsMetaResp `json:"metadata,omitempty"`
}

// SnapshotToResp converts a JetStream perf.PortfolioSnapshot (legacy reader).
// New callers prefer SnapshotRowToResp which carries the full PG row.
func SnapshotToResp(s perf.PortfolioSnapshot) SnapshotResp {
	pos := make([]SnapshotPositionResp, len(s.Positions))
	for i, p := range s.Positions {
		pos[i] = SnapshotPositionResp{Symbol: p.Symbol, Side: p.Side, Qty: p.Qty, AvgPrice: p.AvgPrice}
	}
	return SnapshotResp{HelmID: s.HelmID, TS: s.TS, Cash: s.Cash, Positions: pos}
}

// SnapshotRowToResp converts a PG perflog.SnapshotRow into SnapshotResp.
// Positions are decoded from JSONB; on decode failure the slice stays empty
// (row is still returned so the audit view doesn't lose context).
func SnapshotRowToResp(s readmodel.SnapshotRow) SnapshotResp {
	r := SnapshotResp{
		HelmID:        s.HelmID.String(),
		TS:            s.TS,
		Cash:          s.Cash,
		Equity:        s.Equity,
		RealizedPnL:   s.RealizedPnL,
		UnrealizedPnL: s.UnrealizedPnL,
	}
	if s.HandID != nil {
		r.HandID = s.HandID.String()
	}
	if len(s.PositionsJSON) > 0 {
		var entries []perf.PositionEntry
		if err := json.Unmarshal(s.PositionsJSON, &entries); err == nil {
			r.Positions = make([]SnapshotPositionResp, len(entries))
			for i, e := range entries {
				r.Positions[i] = SnapshotPositionResp{Symbol: e.Symbol, Side: e.Side, Qty: e.Qty, AvgPrice: e.AvgPrice}
			}
		}
	}
	return r
}

// ── Equity ─────────────────────────────────────────────────────────────────

// EquityPointResp is one bucket of the forward-filled equity curve.
// `ts` is the bucket boundary (aligned to the requested resolution).
// `realized_pnl` / `unrealized_pnl` are zero before the first snapshot in the period.
type EquityPointResp struct {
	HandID        string    `json:"hand_id,omitempty"`
	TS            time.Time `json:"ts"`
	Equity        float64   `json:"equity"`
	Cash          float64   `json:"cash"`
	RealizedPnL   float64   `json:"realized_pnl"`
	UnrealizedPnL float64   `json:"unrealized_pnl"`
}

type EquityPageResp struct {
	Points   []EquityPointResp `json:"points"`
	Next     string            `json:"next,omitempty"`
	HasMore  bool              `json:"has_more"`
	Limit    int               `json:"limit"`
	Metadata AnalyticsMetaResp `json:"metadata,omitempty"`
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

// SumAllocatedCapital sums AllocatedCapital across the given hand summaries.
// Returns zero if hands is empty.
func SumAllocatedCapital(hands []handdomain.HandSummary) decimal.Decimal {
	total := decimal.Zero
	for i := range hands {
		total = total.Add(hands[i].AllocatedCapital)
	}
	return total
}

// SumAvailableCash sums AvailableCash (allocated + realized PnL - deployed) across hands.
// Used for unallocated capital: cash − sum(hand.available_cash) = truly free capital.
func SumAvailableCash(hands []handdomain.HandSummary) decimal.Decimal {
	total := decimal.Zero
	for i := range hands {
		total = total.Add(hands[i].AvailableCash)
	}
	return total
}

// PortfolioToResp converts a portfolio.Summary into PortfolioResp.
// allocatedToHands = sum of initial budgets (for display).
// sumHandCash = sum of hand.AvailableCash (allocated + PnL - deployed); used for
// unallocated = cash − sumHandCash, which reflects actual remaining free capital.
func PortfolioToResp(s portfolio.Summary, allocatedToHands, sumHandCash decimal.Decimal) PortfolioResp {
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
	unallocated := s.Cash.Sub(sumHandCash)
	if unallocated.IsNegative() {
		unallocated = decimal.Zero
	}
	return PortfolioResp{
		InitialCapital:     s.InitialCapital,
		Cash:               s.Cash,
		Equity:             s.Equity,
		DeployedCapital:    s.DeployedCapital,
		UnrealizedPnL:      s.UnrealizedPnL,
		RealizedPnL:        s.RealizedPnL,
		AllocatedToHands:   allocatedToHands,
		UnallocatedCapital: unallocated,
		AvailableCash:      s.AvailableCash, // deprecated alias = Cash
		TotalReturn:        s.TotalReturn,
		CurrentDD:          s.CurrentDD,
		MaxDD:              s.MaxDD,
		WinRate:            s.WinRate,
		TotalTrades:        s.TotalTrades,
		OpenPositions:      s.OpenPositions,
		DailyPnL:           s.DailyPnL,
		Positions:          positions,
	}
}

// TradesToResp converts in-memory portfolio.Trade values (used by the live runtime
// before persistence) to TradeResp. PG-only analytical fields are left zero;
// reader endpoints should prefer TradelogToResp for full enrichment.
func TradesToResp(trades []portfolio.Trade) []TradeResp {
	out := make([]TradeResp, len(trades))
	for i, t := range trades {
		out[i] = TradeResp{
			HandID:     t.HandID,
			Symbol:     t.Symbol,
			Side:       string(t.Side),
			Qty:        t.Qty,
			EntryPrice: t.EntryPrice,
			ExitPrice:  t.ExitPrice,
			EntryTime:  t.EntryTimestamp,
			ExitTime:   t.ExitTimestamp,
			GrossPnL:   t.PnL,
			NetPnL:     t.PnL, // commission unknown in-memory; net == gross
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
