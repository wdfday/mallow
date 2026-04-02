use crate::metrics;
use alm_core::portfolio::Portfolio;
use serde::{Deserialize, Serialize};

/// Full backtest result — trading metrics for strategy evaluation.
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
}

impl BacktestReport {
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

        let daily_returns = metrics::daily_returns(&equity);
        let ann_vol = metrics::std_dev(&daily_returns) * (252_f64).sqrt() * 100.0;

        let risk_free_daily = (1.0 + risk_free_annual).powf(1.0 / 252.0) - 1.0;
        let (sharpe, sortino) = metrics::sharpe_sortino(&daily_returns, risk_free_daily);

        let (max_dd, max_dd_bars, avg_dd) = metrics::drawdown_stats(&equity);
        let calmar = if max_dd.abs() > f64::EPSILON {
            cagr / (max_dd * 100.0)
        } else {
            0.0
        };

        let trades = &portfolio.trades;
        let total_trades = trades.len();

        let (win_rate, pf, expectancy, avg_win, avg_loss) = metrics::trade_stats(trades);
        let avg_duration = if total_trades > 0 {
            trades.iter().map(|t| t.duration_hours()).sum::<f64>() / total_trades as f64
        } else {
            0.0
        };
        let max_consec_losses = metrics::max_consecutive_losses(trades);

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
            max_consecutive_losses: max_consec_losses,
        }
    }
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
