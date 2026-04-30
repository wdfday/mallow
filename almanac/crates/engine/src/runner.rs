use crate::engine::Engine;
use alm_core::exit::ExitRules;
use alm_core::strategy::{RiskManager, Strategy};
use alm_data::BarVecFeed;
use alm_report::BacktestReport;
use rayon::prelude::*;

/// Parameters for a single backtest run.
pub struct BacktestJob<S: Strategy, R: RiskManager> {
    pub id: String,
    pub strategy: S,
    pub risk: R,
    pub initial_capital: f64,
    pub commission_pct: f64,
    pub slippage_pct: f64,
    pub risk_free_annual: f64,
}

/// Per-symbol input for portfolio run.
pub struct SymbolBars {
    pub symbol: String,
    pub bars: Vec<alm_core::Bar>,
}

/// Portfolio run result.
pub struct PortfolioReport {
    /// Per-symbol results.
    pub symbols: Vec<(String, BacktestReport)>,
    /// Combined portfolio metrics (capital-weighted).
    pub total_return_pct: f64,
    pub avg_sharpe: f64,
    pub avg_win_rate: f64,
    pub total_trades: usize,
    pub best_symbol: String,
    pub worst_symbol: String,
}

impl PortfolioReport {
    fn compute(results: Vec<(String, BacktestReport)>) -> Self {
        let n = results.len() as f64;
        let total_return_pct = results.iter().map(|(_, r)| r.total_return_pct).sum::<f64>() / n;
        let avg_sharpe = results.iter().map(|(_, r)| r.sharpe_ratio).sum::<f64>() / n;
        let avg_win_rate = results.iter().map(|(_, r)| r.win_rate_pct).sum::<f64>() / n;
        let total_trades = results.iter().map(|(_, r)| r.total_trades).sum();
        let best_symbol = results
            .iter()
            .max_by(|a, b| a.1.total_return_pct.partial_cmp(&b.1.total_return_pct).unwrap())
            .map(|(s, _)| s.clone())
            .unwrap_or_default();
        let worst_symbol = results
            .iter()
            .min_by(|a, b| a.1.total_return_pct.partial_cmp(&b.1.total_return_pct).unwrap())
            .map(|(s, _)| s.clone())
            .unwrap_or_default();
        PortfolioReport { symbols: results, total_return_pct, avg_sharpe, avg_win_rate, total_trades, best_symbol, worst_symbol }
    }
}

/// Run same strategy on multiple symbols in parallel (portfolio backtest).
///
/// Each symbol gets `initial_capital / n_symbols` capital.
/// Uses rayon for parallelism — all symbols run simultaneously.
pub fn run_portfolio<S, R, FS, FR>(
    symbol_bars: Vec<SymbolBars>,
    strategy_factory: FS,
    risk_factory: FR,
    initial_capital: f64,
    commission_pct: f64,
    slippage_pct: f64,
    exit_rules: ExitRules,
    risk_free_annual: f64,
) -> PortfolioReport
where
    S: Strategy + Send + 'static,
    R: RiskManager + Send + 'static,
    FS: Fn() -> S + Sync,
    FR: Fn() -> R + Sync,
{
    let capital_per_symbol = initial_capital / symbol_bars.len() as f64;

    let results: Vec<(String, BacktestReport)> = symbol_bars
        .into_par_iter()
        .map(|sb| {
            let mut feed = BarVecFeed::new(sb.bars, sb.symbol.clone());
            let mut engine = Engine::sync(
                capital_per_symbol,
                strategy_factory(),
                risk_factory(),
                commission_pct,
                slippage_pct,
            )
            .with_exit_rules(exit_rules.clone());
            let report = engine.run(&mut feed, risk_free_annual);
            (sb.symbol, report)
        })
        .collect();

    PortfolioReport::compute(results)
}

/// Run a batch of backtest jobs in parallel using rayon.
///
/// Uses `Engine::sync()` (VecDeque) — no tokio runtime needed per job.
/// All jobs share the same pre-loaded bar data to avoid repeated IO.
pub fn run_batch<S, R>(
    jobs: Vec<BacktestJob<S, R>>,
    bars: Vec<alm_core::Bar>,
    symbol: &str,
) -> Vec<(String, BacktestReport)>
where
    S: Strategy + Send + 'static,
    R: RiskManager + Send + 'static,
{
    jobs.into_par_iter()
        .map(|job| {
            let mut feed = BarVecFeed::new(bars.clone(), symbol.to_string());
            let mut engine = Engine::sync(
                job.initial_capital,
                job.strategy,
                job.risk,
                job.commission_pct,
                job.slippage_pct,
            );
            let report = engine.run(&mut feed, job.risk_free_annual);
            (job.id, report)
        })
        .collect()
}
