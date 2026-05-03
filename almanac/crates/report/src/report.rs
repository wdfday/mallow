use crate::metrics::{self, ulcer_index, serenity_ratio, kelly_pct, trades_per_year, yearly_returns, DirectionStats,
    payoff_ratio, breakeven_win_rate, gross_profit_loss_usd, avg_bars_held_by_outcome, mfe_capture_ratio};
use alm_core::{exit::ExitReason, portfolio::Portfolio, regime::RegimeSummary, Timeframe};
use serde::{Deserialize, Serialize};

/// Breakdown of trade closures by exit type.
///
/// Each field counts the number of trades that were exited via that particular
/// [`alm_core::exit::ExitReason`] variant. Populated by [`BacktestReport::generate`].
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ExitReasonBreakdown {
    pub signal: usize,
    pub stop_loss: usize,
    pub take_profit: usize,
    pub trailing_stop: usize,
    pub atr_stop: usize,
    pub atr_target: usize,
    pub max_bars: usize,
    pub end_of_data: usize,
}

/// Complete performance report produced after a backtest run.
///
/// Aggregates every relevant metric for evaluating a trading strategy:
/// return, risk-adjusted ratios, drawdown, per-trade statistics, distribution
/// shape, rolling series, and regime context.
///
/// Produced by [`BacktestReport::generate`] from a completed [`alm_core::portfolio::Portfolio`].
/// The `regime_summary` field is filled in separately by the engine after `run()` completes.
///
/// All `_pct` scalar fields are in **percentage points** (e.g. `20.0` = 20%), except
/// `win_rate_pct` which is also in percentage points. `psr` is a probability `[0, 1]`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BacktestReport {
    pub strategy: String,
    pub symbol: String,

    // Capital
    pub initial_capital: f64,
    pub final_equity: f64,

    // Return metrics
    pub total_return_pct: f64,
    pub cagr_pct: f64,
    pub annualized_volatility_pct: f64,

    // Risk-adjusted
    pub sharpe_ratio: f64,
    pub sortino_ratio: f64,
    pub calmar_ratio: f64,

    // Drawdown
    pub max_drawdown_pct: f64,
    pub max_dd_duration_bars: usize,
    pub avg_drawdown_pct: f64,

    // Trade stats
    pub total_trades: usize,
    pub win_rate_pct: f64,
    pub profit_factor: f64,
    pub expectancy: f64,
    pub avg_win_pct: f64,
    pub avg_loss_pct: f64,
    pub avg_trade_duration_hours: f64,
    pub max_consecutive_losses: usize,

    // Advanced risk metrics
    pub var_95: f64,
    pub cvar_95: f64,
    pub omega_ratio: f64,
    pub tail_ratio: f64,
    pub recovery_factor: f64,

    // Return distribution shape
    pub skewness: f64,
    pub excess_kurtosis: f64,
    /// P(true Sharpe > 0) — probability this strategy has genuine edge.
    pub psr: f64,

    // Extended trade metrics
    pub max_consecutive_wins: usize,
    pub largest_win_pct: f64,
    pub largest_loss_pct: f64,
    pub sqn: f64,

    // Long / short breakdown
    pub long_stats: DirectionStats,
    pub short_stats: DirectionStats,

    // Monthly returns: (year, month 1–12, return_pct)
    pub monthly_returns: Vec<(i32, u32, f64)>,

    // Rolling metrics (per-bar arrays, window = 30)
    pub rolling_sharpe: Vec<f64>,
    pub rolling_drawdown: Vec<f64>,

    // Detected bar timeframe (from equity curve timestamps)
    pub timeframe: Timeframe,

    // Regime summary (populated by Engine after run)
    pub regime_summary: Option<RegimeSummary>,

    // Drawdown quality
    pub ulcer_index: f64,
    pub serenity_ratio: f64,

    // Position sizing guidance
    pub kelly_pct: f64,

    // Activity
    pub trades_per_year: f64,

    // Cost
    pub total_commission_paid: f64,

    // Yearly return breakdown: (year, annual_return_pct)
    pub yearly_returns: Vec<(i32, f64)>,

    // MAE/MFE averages across all trades (populated by engine via PositionTracker)
    pub avg_mae_pct: f64,
    pub avg_mfe_pct: f64,

    // Extended trade quality metrics
    pub payoff_ratio: f64,
    pub breakeven_win_rate_pct: f64,
    pub gross_profit_usd: f64,
    pub gross_loss_usd: f64,
    pub avg_bars_held_winners: f64,
    pub avg_bars_held_losers: f64,
    pub mfe_capture_ratio: f64,
    pub mae_mfe_ratio: f64,
    pub rolling_sharpe_std: f64,

    /// How many trades were closed by each exit type.
    pub exit_reasons: ExitReasonBreakdown,
}

impl BacktestReport {
    /// Build a complete [`BacktestReport`] from a finished backtest portfolio.
    ///
    /// - `strategy_name` — label stored in `report.strategy` for display/serialisation.
    /// - `symbol` — label stored in `report.symbol`.
    /// - `portfolio` — completed portfolio with `equity_curve` and `trades` populated by the engine.
    /// - `risk_free_annual` — annualised risk-free rate as a fraction (e.g. `0.05` = 5%).
    ///   Used to compute excess returns for Sharpe and Sortino.
    ///
    /// Sharpe and Sortino are computed on **daily** returns (equity aggregated to one value per
    /// calendar day) regardless of bar frequency, so M1 and D1 backtests remain comparable.
    /// The `annualization_factor` is derived empirically from the equity curve timestamps:
    /// US stocks → ~252, crypto → ~365.
    ///
    /// When `portfolio.trades` is empty, Sharpe, Sortino, and PSR are forced to `0.0` to
    /// avoid noise from a flat equity curve against a non-zero risk-free rate.
    pub fn generate(
        strategy_name: &str,
        symbol: &str,
        portfolio: &Portfolio,
        risk_free_annual: f64,
    ) -> Self {
        let equity: Vec<f64> = portfolio.equity_curve.iter().map(|p| p.equity).collect();
        let n = equity.len();

        let initial = portfolio.initial_capital;
        let final_eq = equity.last().copied().unwrap_or(initial);
        let total_return = (final_eq - initial) / initial * 100.0;

        // Duration in years from equity curve timestamps
        let duration_years = if n > 1 {
            let start = portfolio.equity_curve.first().unwrap().timestamp;
            let end = portfolio.equity_curve.last().unwrap().timestamp;
            (end - start) as f64 / (365.25 * 24.0 * 3600.0 * 1000.0)
        } else {
            1.0
        };

        let cagr = if duration_years > 0.0 {
            ((final_eq / initial).powf(1.0 / duration_years) - 1.0) * 100.0
        } else {
            0.0
        };

        // Detect timeframe from equity curve timestamps.
        let timeframe = Timeframe::detect(
            &portfolio.equity_curve.iter().map(|p| p.timestamp).collect::<Vec<_>>(),
        );

        // --- Annualization via daily returns -----------------------------------
        // Aggregate the equity curve to one value per calendar day (last bar of day).
        // Sharpe/Sortino are always expressed in daily units regardless of bar freq:
        //   - M1 and H1 backtests become comparable
        //   - Consecutive identical bars (no open position) don't inflate variance
        // `days_per_year` is empirical (actual trading days / calendar years):
        //   US stocks → ~252, crypto → ~365, both correct automatically.
        let (daily_equity, days_per_year) = aggregate_to_daily(portfolio);
        let daily_returns = metrics::bar_returns(&daily_equity);

        let ann_vol = metrics::std_dev(&daily_returns) * days_per_year.sqrt() * 100.0;

        let risk_free_daily = (1.0 + risk_free_annual).powf(1.0 / days_per_year) - 1.0;
        let (sharpe_raw, sortino_raw) = metrics::sharpe_sortino(&daily_returns, risk_free_daily, days_per_year);

        let (max_dd, max_dd_bars, avg_dd) = metrics::drawdown_stats(&equity);
        let calmar = if max_dd.abs() > f64::EPSILON {
            cagr / (max_dd * 100.0)
        } else {
            0.0
        };

        let trades = &portfolio.trades;
        let total_trades = trades.len();

        // Guard: ratio metrics are meaningless (and noisy) with no trades.
        // e.g. flat equity + positive risk-free → negative mean excess → negative Sortino.
        let no_trades = total_trades == 0;
        let sharpe  = if no_trades { 0.0 } else { sharpe_raw };
        let sortino = if no_trades { 0.0 } else { sortino_raw };

        let (win_rate, pf, expectancy, avg_win, avg_loss) = metrics::trade_stats(trades);
        let avg_duration = if total_trades > 0 {
            trades.iter().map(|t| t.duration_hours()).sum::<f64>() / total_trades as f64
        } else {
            0.0
        };
        let skewness       = metrics::skewness(&daily_returns);
        let excess_kurtosis = metrics::excess_kurtosis(&daily_returns);
        let psr            = if no_trades { 0.0 } else { metrics::psr(&daily_returns, 0.0) };

        let max_consec_losses = metrics::max_consecutive_losses(trades);
        let max_consec_wins   = metrics::max_consecutive_wins(trades);
        let (largest_win, largest_loss) = metrics::largest_win_loss(trades);
        let sqn = metrics::sqn(trades);
        let (long_stats, short_stats) = metrics::direction_stats(trades);
        let monthly_returns = metrics::monthly_returns(
            &portfolio.equity_curve.iter().map(|p| (p.timestamp, p.equity)).collect::<Vec<_>>(),
        );

        // Advanced risk metrics — also daily-basis for consistency
        let (var_95, cvar_95) = metrics::var_cvar_95(&daily_returns);
        let omega = metrics::omega_ratio(&daily_returns, 0.0);
        let tail = metrics::tail_ratio(&daily_returns);
        let recovery = metrics::recovery_factor(total_return / 100.0, max_dd);

        // Rolling Sharpe uses bar-level granularity (visual sparkline).
        // ann_factor = days_per_year keeps the scale consistent with headline Sharpe.
        let rolling_sharpe = metrics::rolling_sharpe(&equity, 30, days_per_year);
        let rolling_drawdown = metrics::rolling_drawdown(&equity);

        let ui = ulcer_index(&equity);
        let serenity = serenity_ratio(cagr, ui);
        let kelly = kelly_pct(win_rate, avg_win * 100.0, avg_loss.abs() * 100.0);
        let tpy = trades_per_year(total_trades, duration_years);
        let total_commission: f64 = trades.iter().map(|t| t.commission).sum();
        let yearly = yearly_returns(&monthly_returns);
        let (avg_mae, avg_mfe) = if total_trades > 0 {
            let mae_sum: f64 = trades.iter().map(|t| t.mae_pct).sum();
            let mfe_sum: f64 = trades.iter().map(|t| t.mfe_pct).sum();
            (mae_sum / total_trades as f64 * 100.0, mfe_sum / total_trades as f64 * 100.0)
        } else {
            (0.0, 0.0)
        };

        let pr = payoff_ratio(avg_win, avg_loss);
        let bew = breakeven_win_rate(pr);
        let (gross_profit, gross_loss) = gross_profit_loss_usd(trades);
        let (avg_bars_winners, avg_bars_losers) = avg_bars_held_by_outcome(trades);
        let mfe_capture = mfe_capture_ratio(trades);
        let mae_mfe = if avg_mfe.abs() > f64::EPSILON { avg_mae / avg_mfe } else { 0.0 };
        let rolling_sharpe_std = metrics::std_dev(&metrics::rolling_sharpe(&equity, 30, days_per_year));

        let mut exit_reasons = ExitReasonBreakdown::default();
        for t in trades.iter() {
            match t.exit_reason {
                ExitReason::Signal       => exit_reasons.signal += 1,
                ExitReason::StopLoss     => exit_reasons.stop_loss += 1,
                ExitReason::TakeProfit   => exit_reasons.take_profit += 1,
                ExitReason::TrailingStop => exit_reasons.trailing_stop += 1,
                ExitReason::AtrStop      => exit_reasons.atr_stop += 1,
                ExitReason::AtrTarget    => exit_reasons.atr_target += 1,
                ExitReason::MaxBarsHeld  => exit_reasons.max_bars += 1,
                ExitReason::EndOfData    => exit_reasons.end_of_data += 1,
            }
        }

        Self {
            strategy: strategy_name.to_string(),
            symbol: symbol.to_string(),
            initial_capital: initial,
            final_equity: final_eq,
            total_return_pct: total_return,
            cagr_pct: cagr,
            annualized_volatility_pct: ann_vol,
            sharpe_ratio: sharpe,
            sortino_ratio: sortino,
            calmar_ratio: calmar,
            max_drawdown_pct: max_dd * 100.0,
            max_dd_duration_bars: max_dd_bars,
            avg_drawdown_pct: avg_dd * 100.0,
            total_trades,
            win_rate_pct: win_rate * 100.0,
            profit_factor: pf,
            expectancy,
            avg_win_pct: avg_win * 100.0,
            avg_loss_pct: avg_loss * 100.0,
            avg_trade_duration_hours: avg_duration,
            skewness,
            excess_kurtosis,
            psr,
            max_consecutive_losses: max_consec_losses,
            max_consecutive_wins: max_consec_wins,
            largest_win_pct: largest_win * 100.0,
            largest_loss_pct: largest_loss * 100.0,
            sqn,
            long_stats,
            short_stats,
            monthly_returns,
            var_95,
            cvar_95,
            omega_ratio: omega,
            tail_ratio: tail,
            recovery_factor: recovery,
            rolling_sharpe,
            rolling_drawdown,
            timeframe,
            regime_summary: None, // populated by Engine after run()
            ulcer_index: ui,
            serenity_ratio: serenity,
            kelly_pct: kelly,
            trades_per_year: tpy,
            total_commission_paid: total_commission,
            yearly_returns: yearly,
            avg_mae_pct: avg_mae,
            avg_mfe_pct: avg_mfe,
            payoff_ratio: pr,
            breakeven_win_rate_pct: bew,
            gross_profit_usd: gross_profit,
            gross_loss_usd: gross_loss,
            avg_bars_held_winners: avg_bars_winners,
            avg_bars_held_losers: avg_bars_losers,
            mfe_capture_ratio: mfe_capture,
            mae_mfe_ratio: mae_mfe,
            rolling_sharpe_std,
            exit_reasons,
        }
    }
}

/// Collapse the equity curve to one value per calendar day (last bar of each day),
/// and return the daily-equity series alongside an empirical `days_per_year`.
///
/// `days_per_year` = actual trading days observed / elapsed calendar years:
///   - US stocks → ~252 (weekdays only)
///   - Crypto    → ~365 (24/7)
///   - Both correct without any hardcoded constant
///
/// Using daily returns as the base unit for Sharpe/Sortino:
///   1. Makes M1 and H1 backtests directly comparable
///   2. Avoids the variance inflation from consecutive zero-return bars
///      (equity unchanged while no position is open)
fn aggregate_to_daily(portfolio: &Portfolio) -> (Vec<f64>, f64) {
    use std::collections::BTreeMap;

    if portfolio.equity_curve.is_empty() {
        return (vec![], 252.0);
    }

    // Key = calendar day (ms → day index), value = last equity of that day.
    let mut days: BTreeMap<i64, f64> = BTreeMap::new();
    for p in &portfolio.equity_curve {
        let day = p.timestamp / 86_400_000;
        days.insert(day, p.equity);
    }

    let daily_equity: Vec<f64> = days.values().copied().collect();
    let n = daily_equity.len();
    if n < 2 {
        return (daily_equity, 252.0);
    }

    let first_day = *days.keys().next().unwrap() as f64;
    let last_day  = *days.keys().last().unwrap() as f64;
    let elapsed_years = (last_day - first_day) / 365.25;  // day index units
    let days_per_year = if elapsed_years > 1e-6 {
        (n - 1) as f64 / elapsed_years
    } else {
        252.0
    };

    (daily_equity, days_per_year)
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::{
        order::Side,
        portfolio::{EquityPoint, Portfolio},
        trade::Trade,
    };

    fn make_trade(pnl: f64, pnl_pct: f64, entry_ts: i64, exit_ts: i64) -> Trade {
        Trade {
            symbol: "TEST".into(),
            side: Side::Buy,
            qty: 1.0,
            entry_price: 100.0,
            exit_price: 100.0 + pnl,
            entry_timestamp: entry_ts,
            exit_timestamp: exit_ts,
            pnl,
            pnl_pct,
            commission: 0.0,
            mae_pct: 0.0,
            mfe_pct: 0.0,
            bars_held: 0,
            exit_reason: alm_core::exit::ExitReason::Signal,
        }
    }

    fn make_portfolio(equity_values: &[(i64, f64)], trades: Vec<Trade>) -> Portfolio {
        let mut p = Portfolio::new(equity_values[0].1);
        p.equity_curve = equity_values
            .iter()
            .map(|&(ts, eq)| EquityPoint { timestamp: ts, equity: eq })
            .collect();
        p.trades = trades;
        p
    }

    #[test]
    fn report_empty_portfolio() {
        let p = make_portfolio(&[(0, 10_000.0)], vec![]);
        let r = BacktestReport::generate("test", "SYM", &p, 0.0);
        assert_eq!(r.total_trades, 0);
        assert_eq!(r.final_equity, 10_000.0);
        assert_eq!(r.total_return_pct, 0.0);
        assert_eq!(r.win_rate_pct, 0.0);
    }

    #[test]
    fn report_profitable() {
        let day_ms = 86_400_000i64;
        let p = make_portfolio(
            &[(0, 10_000.0), (day_ms * 100, 12_000.0)],
            vec![
                make_trade(500.0, 0.05, 0, day_ms * 30),
                make_trade(500.0, 0.05, day_ms * 40, day_ms * 70),
            ],
        );
        let r = BacktestReport::generate("strategy", "MBB", &p, 0.0);
        assert_eq!(r.strategy, "strategy");
        assert_eq!(r.symbol, "MBB");
        assert!((r.total_return_pct - 20.0).abs() < 0.01);
        assert_eq!(r.total_trades, 2);
        assert_eq!(r.win_rate_pct, 100.0);
        assert_eq!(r.profit_factor, f64::INFINITY);
        assert_eq!(r.max_consecutive_losses, 0);
    }

    #[test]
    fn report_losing() {
        let day_ms = 86_400_000i64;
        let p = make_portfolio(
            &[(0, 10_000.0), (day_ms * 50, 8_000.0)],
            vec![
                make_trade(-500.0, -0.05, 0, day_ms * 10),
                make_trade(-300.0, -0.03, day_ms * 20, day_ms * 30),
            ],
        );
        let r = BacktestReport::generate("loser", "XYZ", &p, 0.0);
        assert!(r.total_return_pct < 0.0);
        assert_eq!(r.win_rate_pct, 0.0);
        assert_eq!(r.profit_factor, 0.0);
        assert_eq!(r.max_consecutive_losses, 2);
        assert!(r.max_drawdown_pct > 0.0);
    }

    #[test]
    fn report_mixed_trades() {
        let day_ms = 86_400_000i64;
        let trades = vec![
            make_trade(200.0, 0.02, 0, day_ms),
            make_trade(-100.0, -0.01, day_ms * 2, day_ms * 3),
            make_trade(300.0, 0.03, day_ms * 4, day_ms * 5),
            make_trade(-50.0, -0.005, day_ms * 6, day_ms * 7),
            make_trade(-50.0, -0.005, day_ms * 8, day_ms * 9),
        ];
        let p = make_portfolio(&[(0, 10_000.0), (day_ms * 10, 10_300.0)], trades);
        let r = BacktestReport::generate("mixed", "T", &p, 0.0);
        assert_eq!(r.total_trades, 5);
        assert!((r.win_rate_pct - 40.0).abs() < 0.01);
        assert_eq!(r.max_consecutive_losses, 2);
        assert!(r.avg_trade_duration_hours > 0.0);
    }

    #[test]
    fn report_drawdown_accuracy() {
        // 10_000 → 15_000 → 9_000: max DD = (15000-9000)/15000 = 40%
        let day_ms = 86_400_000i64;
        let p = make_portfolio(
            &[(0, 10_000.0), (day_ms, 15_000.0), (day_ms * 2, 9_000.0), (day_ms * 3, 12_000.0)],
            vec![],
        );
        let r = BacktestReport::generate("dd_test", "T", &p, 0.0);
        assert!((r.max_drawdown_pct - 40.0).abs() < 0.01);
    }

    #[test]
    fn report_cagr_approx_one_year() {
        // +20% over ~1 year → CAGR ≈ 20%
        let one_year_ms = (365.25 * 24.0 * 3600.0 * 1000.0) as i64;
        let p = make_portfolio(&[(0, 10_000.0), (one_year_ms, 12_000.0)], vec![]);
        let r = BacktestReport::generate("cagr_test", "T", &p, 0.0);
        assert!((r.cagr_pct - 20.0).abs() < 0.1);
    }
}
