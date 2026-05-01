use crate::metrics;
use crate::report::{BacktestReport};
use alm_core::portfolio::EquityPoint;

/// Per-symbol and cross-symbol analytics for multi-asset portfolio evaluation.
#[derive(Debug, Clone)]
pub struct PortfolioAnalytics {
    /// Symbol names (same order as all per-symbol slices).
    pub symbols: Vec<String>,
    /// Pearson correlation matrix of daily equity-curve returns (n × n).
    pub correlation_matrix: Vec<Vec<f64>>,
    /// Beta of each symbol vs. benchmark.
    pub beta: Vec<f64>,
    /// Annualized alpha of each symbol vs. benchmark (excess return after beta adjustment).
    pub alpha_annualized: Vec<f64>,
    /// Average turnover per bar as % of equity (proxied from trade frequency).
    pub turnover_pct: Vec<f64>,
    /// Diversification ratio: weighted-avg(individual vols) / equal-weight portfolio vol.
    pub diversification_ratio: f64,
}

/// Compute cross-symbol portfolio analytics.
///
/// `symbol_reports` — one entry per symbol: (symbol_name, BacktestReport, equity_curve).
/// `benchmark_equity` — reference equity curve for beta/alpha (e.g. buy-and-hold).
///
/// All equity curves are aligned by index (same length expected after resampling).
/// Shorter curves are zero-padded to the longest length.
pub fn analyze(
    symbol_reports: &[(String, BacktestReport, Vec<EquityPoint>)],
    benchmark_equity: &[f64],
) -> PortfolioAnalytics {
    if symbol_reports.is_empty() {
        return PortfolioAnalytics {
            symbols: vec![],
            correlation_matrix: vec![],
            beta: vec![],
            alpha_annualized: vec![],
            turnover_pct: vec![],
            diversification_ratio: 1.0,
        };
    }

    let n = symbol_reports.len();
    let symbols: Vec<String> = symbol_reports.iter().map(|(s, _, _)| s.clone()).collect();

    // Build aligned daily returns for each symbol.
    let max_len: usize = symbol_reports.iter().map(|(_, _, ec)| ec.len()).max().unwrap_or(0);

    let all_returns: Vec<Vec<f64>> = symbol_reports
        .iter()
        .map(|(_, _, ec)| {
            let equity: Vec<f64> = ec.iter().map(|p| p.equity).collect();
            let mut r = metrics::daily_returns(&equity);
            // Pad with zeros if shorter than max.
            r.resize(max_len.saturating_sub(1), 0.0);
            r
        })
        .collect();

    // Benchmark returns (aligned).
    let mut bench_returns = metrics::daily_returns(benchmark_equity);
    bench_returns.resize(max_len.saturating_sub(1), 0.0);

    // ── Correlation matrix (Pearson) ──────────────────────────────────────────
    let correlation_matrix = pearson_correlation_matrix(&all_returns);

    // ── Beta & Alpha ──────────────────────────────────────────────────────────
    let bench_mean = metrics::mean(&bench_returns);
    let bench_var = variance(&bench_returns);

    let mut beta = vec![0.0_f64; n];
    let mut alpha_annualized = vec![0.0_f64; n];

    for (i, returns) in all_returns.iter().enumerate() {
        let b = if bench_var > f64::EPSILON {
            covariance(returns, &bench_returns) / bench_var
        } else {
            0.0
        };
        beta[i] = b;

        let strat_mean = metrics::mean(returns);
        // Annualized alpha: (mean_daily_excess) × 252
        let alpha_daily = strat_mean - b * bench_mean;
        alpha_annualized[i] = alpha_daily * 252.0;
    }

    // ── Turnover ──────────────────────────────────────────────────────────────
    // Proxy: (total_trades / n_bars) × 100 gives avg position changes per bar as %.
    let turnover_pct: Vec<f64> = symbol_reports
        .iter()
        .map(|(_, report, ec)| {
            let n_bars = ec.len().max(1);
            (report.total_trades as f64 / n_bars as f64) * 100.0
        })
        .collect();

    // ── Diversification ratio ─────────────────────────────────────────────────
    // DR = weighted_avg_vol / portfolio_vol  (equal weights, 1/n each)
    let individual_vols: Vec<f64> = all_returns.iter().map(|r| metrics::std_dev(r)).collect();
    let avg_vol = metrics::mean(&individual_vols);

    // Equal-weight portfolio returns.
    let portfolio_returns: Vec<f64> = (0..bench_returns.len())
        .map(|t| {
            all_returns.iter().map(|r| r.get(t).copied().unwrap_or(0.0)).sum::<f64>()
                / n as f64
        })
        .collect();
    let portfolio_vol = metrics::std_dev(&portfolio_returns);

    let diversification_ratio =
        if portfolio_vol < f64::EPSILON { 1.0 } else { avg_vol / portfolio_vol };

    PortfolioAnalytics {
        symbols,
        correlation_matrix,
        beta,
        alpha_annualized,
        turnover_pct,
        diversification_ratio,
    }
}

// ── Internal helpers ──────────────────────────────────────────────────────────

fn variance(v: &[f64]) -> f64 {
    if v.len() < 2 {
        return 0.0;
    }
    let m = metrics::mean(v);
    v.iter().map(|x| (x - m).powi(2)).sum::<f64>() / (v.len() - 1) as f64
}

fn covariance(a: &[f64], b: &[f64]) -> f64 {
    let len = a.len().min(b.len());
    if len < 2 {
        return 0.0;
    }
    let ma = metrics::mean(&a[..len]);
    let mb = metrics::mean(&b[..len]);
    a[..len].iter().zip(b[..len].iter()).map(|(x, y)| (x - ma) * (y - mb)).sum::<f64>()
        / (len - 1) as f64
}

fn pearson_correlation(a: &[f64], b: &[f64]) -> f64 {
    let cov = covariance(a, b);
    let sa = metrics::std_dev(a);
    let sb = metrics::std_dev(b);
    if sa < f64::EPSILON || sb < f64::EPSILON {
        0.0
    } else {
        (cov / (sa * sb)).clamp(-1.0, 1.0)
    }
}

fn pearson_correlation_matrix(all_returns: &[Vec<f64>]) -> Vec<Vec<f64>> {
    let n = all_returns.len();
    let mut matrix = vec![vec![0.0_f64; n]; n];
    for i in 0..n {
        for j in 0..n {
            matrix[i][j] = if i == j {
                1.0
            } else {
                pearson_correlation(&all_returns[i], &all_returns[j])
            };
        }
    }
    matrix
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::portfolio::EquityPoint;

    fn make_equity(values: &[f64]) -> Vec<EquityPoint> {
        values
            .iter()
            .enumerate()
            .map(|(i, &eq)| EquityPoint { timestamp: i as i64 * 86_400_000, equity: eq })
            .collect()
    }

    fn dummy_report(total_trades: usize) -> BacktestReport {
        BacktestReport {
            strategy: "test".into(),
            symbol: "X".into(),
            initial_capital: 10_000.0,
            final_equity: 10_000.0,
            total_return_pct: 0.0,
            cagr_pct: 0.0,
            annualized_volatility_pct: 0.0,
            sharpe_ratio: 0.0,
            sortino_ratio: 0.0,
            calmar_ratio: 0.0,
            max_drawdown_pct: 0.0,
            max_dd_duration_bars: 0,
            avg_drawdown_pct: 0.0,
            total_trades,
            win_rate_pct: 0.0,
            profit_factor: 0.0,
            expectancy: 0.0,
            avg_win_pct: 0.0,
            avg_loss_pct: 0.0,
            avg_trade_duration_hours: 0.0,
            skewness: 0.0,
            excess_kurtosis: 0.0,
            psr: 0.0,
            max_consecutive_losses: 0,
            max_consecutive_wins: 0,
            largest_win_pct: 0.0,
            largest_loss_pct: 0.0,
            sqn: 0.0,
            long_stats: Default::default(),
            short_stats: Default::default(),
            monthly_returns: vec![],
            var_95: 0.0,
            cvar_95: 0.0,
            omega_ratio: 0.0,
            tail_ratio: 0.0,
            recovery_factor: 0.0,
            rolling_sharpe: vec![],
            rolling_drawdown: vec![],
            timeframe: alm_core::Timeframe::D1,
            regime_summary: None,
            ulcer_index: 0.0,
            serenity_ratio: 0.0,
            kelly_pct: 0.0,
            trades_per_year: 0.0,
            total_commission_paid: 0.0,
            yearly_returns: vec![],
            avg_mae_pct: 0.0,
            avg_mfe_pct: 0.0,
            payoff_ratio: 0.0,
            breakeven_win_rate_pct: 0.0,
            gross_profit_usd: 0.0,
            gross_loss_usd: 0.0,
            avg_bars_held_winners: 0.0,
            avg_bars_held_losers: 0.0,
            mfe_capture_ratio: 0.0,
            mae_mfe_ratio: 0.0,
            rolling_sharpe_std: 0.0,
            exit_reasons: ExitReasonBreakdown::default(),
        }
    }

    #[test]
    fn analyze_empty() {
        let result = analyze(&[], &[]);
        assert!(result.symbols.is_empty());
        assert_eq!(result.diversification_ratio, 1.0);
    }

    #[test]
    fn analyze_single_symbol() {
        let ec = make_equity(&[10_000.0, 10_100.0, 10_200.0, 10_150.0, 10_300.0]);
        let bench = vec![1.0, 1.01, 1.02, 1.015, 1.03];
        let reports = vec![("AAPL".to_string(), dummy_report(5), ec)];
        let analytics = analyze(&reports, &bench);
        assert_eq!(analytics.symbols, vec!["AAPL"]);
        assert_eq!(analytics.correlation_matrix.len(), 1);
        assert!((analytics.correlation_matrix[0][0] - 1.0).abs() < 1e-10);
        assert_eq!(analytics.beta.len(), 1);
    }

    #[test]
    fn analyze_two_symbols_perfectly_correlated() {
        let ec1 = make_equity(&[10_000.0, 10_100.0, 10_200.0, 10_300.0]);
        let ec2 = make_equity(&[10_000.0, 10_100.0, 10_200.0, 10_300.0]); // identical
        let bench = vec![1.0, 1.01, 1.02, 1.03];
        let reports = vec![
            ("A".to_string(), dummy_report(2), ec1),
            ("B".to_string(), dummy_report(2), ec2),
        ];
        let analytics = analyze(&reports, &bench);
        // Perfectly correlated → correlation should be ~1.0
        assert!((analytics.correlation_matrix[0][1] - 1.0).abs() < 1e-6);
        assert!((analytics.correlation_matrix[1][0] - 1.0).abs() < 1e-6);
    }
}
