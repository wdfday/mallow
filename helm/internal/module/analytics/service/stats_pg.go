package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/analytics/domain"
)

// PostgresStatsRunner runs the SQL aggregation pipeline for ComputeStats.
// Single connection; relies on PG's window functions + CTEs for the heavy lifting
// so the Go layer only marshals.
type PostgresStatsRunner struct {
	db *sql.DB
}

// NewPostgresStatsRunner constructs a PostgresStatsRunner.
func NewPostgresStatsRunner(db *sql.DB) *PostgresStatsRunner {
	return &PostgresStatsRunner{db: db}
}

// RunStats executes the stats aggregation against the trades table.
// Returns zeroed Stats when there are no matching trades.
func (r *PostgresStatsRunner) RunStats(ctx context.Context, scope domain.Scope, p domain.Period) (domain.Stats, error) {
	where, args := buildStatsWhere(scope, p)

	stats := domain.Stats{}
	// ── Universal KPIs ────────────────────────────────────────────────────
	mainQuery := `
		WITH t AS (
			SELECT net_pnl, commission, r_multiple, holding_seconds
			FROM trades WHERE ` + where + `
		)
		SELECT
			COUNT(*)                                                   AS n,
			COUNT(*) FILTER (WHERE net_pnl > 0)                        AS wins,
			COALESCE(SUM(net_pnl) FILTER (WHERE net_pnl > 0), 0)       AS gross_profit,
			COALESCE(SUM(-net_pnl) FILTER (WHERE net_pnl < 0), 0)      AS gross_loss_abs,
			COALESCE(SUM(net_pnl), 0)                                  AS net_pnl,
			COALESCE(SUM(commission), 0)                               AS commission,
			COALESCE(AVG(net_pnl) FILTER (WHERE net_pnl > 0), 0)       AS avg_win,
			COALESCE(AVG(net_pnl) FILTER (WHERE net_pnl < 0), 0)       AS avg_loss,
			COALESCE(MAX(net_pnl), 0)                                  AS largest_win,
			COALESCE(MIN(net_pnl), 0)                                  AS largest_loss,
			COALESCE(AVG(r_multiple), 0)                               AS expectancy_r,
			COALESCE(AVG(holding_seconds), 0)::INT                     AS avg_holding_seconds
		FROM t
	`

	var (
		n, wins, avgHold          int
		grossProfit, grossLossAbs sql.NullString
		netPnL, commission        sql.NullString
		avgWin, avgLoss           sql.NullString
		largestWin, largestLoss   sql.NullString
		expectancyR               sql.NullFloat64
	)
	if err := r.db.QueryRowContext(ctx, mainQuery, args...).Scan(
		&n, &wins, &grossProfit, &grossLossAbs, &netPnL, &commission,
		&avgWin, &avgLoss, &largestWin, &largestLoss, &expectancyR, &avgHold,
	); err != nil {
		return stats, fmt.Errorf("stats main query: %w", err)
	}

	stats.NTrades = n
	if n > 0 {
		stats.WinRate = float64(wins) / float64(n)
	}
	stats.GrossProfit = decFromNullStr(grossProfit)
	stats.GrossLoss = decFromNullStr(grossLossAbs)
	stats.NetPnL = decFromNullStr(netPnL)
	stats.Commission = decFromNullStr(commission)
	stats.AvgWin = decFromNullStr(avgWin)
	stats.AvgLoss = decFromNullStr(avgLoss)
	stats.LargestWin = decFromNullStr(largestWin)
	stats.LargestLoss = decFromNullStr(largestLoss)
	stats.ExpectancyR = expectancyR.Float64
	stats.AvgHoldingSeconds = avgHold

	// Profit factor — defined when gross_loss > 0 (otherwise +Inf, which JSON
	// can't represent; we set 0 and let FE interpret n_trades + gross_loss).
	if stats.GrossLoss.IsPositive() {
		pf, _ := stats.GrossProfit.Div(stats.GrossLoss).Float64()
		stats.ProfitFactor = pf
	}

	// Expectancy in $ — derived from win_rate × avg_win + (1−win_rate) × avg_loss.
	wr := decimal.NewFromFloat(stats.WinRate)
	one := decimal.NewFromInt(1)
	stats.Expectancy = wr.Mul(stats.AvgWin).Add(one.Sub(wr).Mul(stats.AvgLoss))

	// ── Attribution slices (3 × group-by) ─────────────────────────────────
	bySym, err := r.runGrouped(ctx, "symbol", where, args)
	if err != nil {
		return stats, fmt.Errorf("by_symbol: %w", err)
	}
	stats.BySymbol = bySym

	byExit, err := r.runGrouped(ctx, "source", where, args)
	if err != nil {
		return stats, fmt.Errorf("by_exit: %w", err)
	}
	stats.ByExit = byExit

	return stats, nil
}

// runGrouped runs a single per-column GROUP BY for attribution slices.
// Filters out NULL groups (we don't surface "unattributed" rows in stats today).
func (r *PostgresStatsRunner) runGrouped(ctx context.Context, col, where string, args []any) ([]domain.GroupedKPI, error) {
	q := fmt.Sprintf(`
		SELECT %s,
		       COUNT(*),
		       COUNT(*) FILTER (WHERE net_pnl > 0),
		       COALESCE(SUM(net_pnl), 0),
		       COALESCE(AVG(r_multiple), 0)
		FROM trades
		WHERE %s AND %s IS NOT NULL
		GROUP BY %s
		ORDER BY COUNT(*) DESC
		LIMIT 50
	`, col, where, col, col)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.GroupedKPI, 0, 8)
	for rows.Next() {
		var key sql.NullString
		var n, wins int
		var netPnL sql.NullString
		var avgR sql.NullFloat64
		if err := rows.Scan(&key, &n, &wins, &netPnL, &avgR); err != nil {
			return nil, err
		}
		g := domain.GroupedKPI{
			Key:     key.String,
			NTrades: n,
			NetPnL:  decFromNullStr(netPnL),
			AvgR:    avgR.Float64,
		}
		if n > 0 {
			g.WinRate = float64(wins) / float64(n)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// buildStatsWhere assembles the parameterised WHERE clause shared by the main
// query and the grouped queries. Order: user_id, [helm_id], [hand_id], [after], [before].
func buildStatsWhere(scope domain.Scope, p domain.Period) (string, []any) {
	parts := []string{"user_id = $1"}
	args := []any{scope.UserID}
	idx := 2
	if scope.HelmID != nil {
		parts = append(parts, fmt.Sprintf("helm_id = $%d", idx))
		args = append(args, *scope.HelmID)
		idx++
	}
	if scope.HandID != nil {
		parts = append(parts, fmt.Sprintf("hand_id = $%d", idx))
		args = append(args, *scope.HandID)
		idx++
	}
	if !p.After.IsZero() {
		parts = append(parts, fmt.Sprintf("exit_at >= $%d", idx))
		args = append(args, p.After)
		idx++
	}
	if !p.Before.IsZero() {
		parts = append(parts, fmt.Sprintf("exit_at < $%d", idx))
		args = append(args, p.Before)
	}
	return strings.Join(parts, " AND "), args
}

func decFromNullStr(ns sql.NullString) decimal.Decimal {
	if !ns.Valid {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(ns.String)
	if err != nil {
		return decimal.Zero
	}
	return d
}

// Ensure imports compile when time is unused depending on build.
var _ = time.Time{}
