use crate::bus_sync::SyncBus;
use crate::engine::Engine;
use alm_core::{
    bar::Bar,
    bus::EventBus,
    strategy::{RiskManager, Strategy},
};
use alm_data::InMemoryFeed;
use alm_report::BacktestReport;

/// Walk-forward validation config.
///
/// Rolling windows: each window slides forward by `step_bars`.
/// OOS windows never overlap → combined OOS gives unbiased estimate.
///
/// ```text
/// Window 0:  IS=[0..is_bars]             OOS=[is_bars..is_bars+oos_bars]
/// Window 1:  IS=[step..is_bars+step]     OOS=[is_bars+step..is_bars+step+oos_bars]
/// ...
/// ```
pub struct WalkForwardConfig {
    /// Number of bars in each in-sample window.
    pub is_bars: usize,
    /// Number of bars in each out-of-sample window.
    pub oos_bars: usize,
    /// How many bars to advance per step (default = oos_bars → non-overlapping OOS).
    pub step_bars: usize,
    /// Initial capital for each window (engine is fully reset between windows).
    pub initial_capital: f64,
    /// Risk-free annual rate for Sharpe calculation.
    pub risk_free_annual: f64,
}

impl WalkForwardConfig {
    pub fn new(is_bars: usize, oos_bars: usize, initial_capital: f64) -> Self {
        Self {
            is_bars,
            oos_bars,
            step_bars: oos_bars, // non-overlapping OOS by default
            initial_capital,
            risk_free_annual: 0.04,
        }
    }
}

/// Single walk-forward window result.
pub struct WalkForwardWindow {
    pub window: usize,
    /// Bar index range for in-sample period [start, end).
    pub is_range: (usize, usize),
    /// Bar index range for out-of-sample period [start, end).
    pub oos_range: (usize, usize),
    pub in_sample: BacktestReport,
    pub out_of_sample: BacktestReport,
}

/// Aggregate walk-forward results.
pub struct WalkForwardResult {
    pub windows: Vec<WalkForwardWindow>,
    /// Average OOS Sharpe across all windows.
    pub avg_oos_sharpe: f64,
    /// Average OOS win rate (%).
    pub avg_oos_win_rate: f64,
    /// Total OOS trades across all windows.
    pub total_oos_trades: usize,
    /// Average OOS total return (%) across windows.
    pub avg_oos_return_pct: f64,
    /// Fraction of windows where OOS Sharpe > 0.
    pub pct_profitable_windows: f64,
    /// Efficiency ratio: avg OOS Sharpe / avg IS Sharpe (1.0 = perfect, <0.5 = overfitting).
    pub efficiency_ratio: f64,
}

/// Run rolling walk-forward validation on a pre-loaded bar slice.
///
/// The engine is fully reset between windows (capital, strategy state, positions).
/// No parameter optimization is performed — this tests strategy *consistency*.
pub fn walk_forward<S, R, B>(
    engine: &mut Engine<S, R, B>,
    bars: &[Bar],
    symbol: &str,
    cfg: WalkForwardConfig,
) -> WalkForwardResult
where
    S: Strategy,
    R: RiskManager,
    B: EventBus,
{
    let n = bars.len();
    let mut windows = Vec::new();
    let mut start = 0;
    let mut window_idx = 0;

    while start + cfg.is_bars + cfg.oos_bars <= n {
        let is_end = start + cfg.is_bars;
        let oos_end = is_end + cfg.oos_bars;

        // ── In-sample ────────────────────────────────────────────────────────
        engine.reset(cfg.initial_capital);
        let mut is_feed =
            InMemoryFeed::new(bars[start..is_end].to_vec(), symbol.to_string());
        let is_report = engine.run(&mut is_feed, cfg.risk_free_annual);

        // ── Out-of-sample ─────────────────────────────────────────────────────
        // Full reset: strategy enters OOS cold (no carry-over from IS).
        engine.reset(cfg.initial_capital);
        let mut oos_feed =
            InMemoryFeed::new(bars[is_end..oos_end].to_vec(), symbol.to_string());
        let oos_report = engine.run(&mut oos_feed, cfg.risk_free_annual);

        windows.push(WalkForwardWindow {
            window: window_idx,
            is_range: (start, is_end),
            oos_range: (is_end, oos_end),
            in_sample: is_report,
            out_of_sample: oos_report,
        });

        start += cfg.step_bars;
        window_idx += 1;
    }

    if windows.is_empty() {
        return WalkForwardResult {
            windows,
            avg_oos_sharpe: 0.0,
            avg_oos_win_rate: 0.0,
            total_oos_trades: 0,
            avg_oos_return_pct: 0.0,
            pct_profitable_windows: 0.0,
            efficiency_ratio: 0.0,
        };
    }

    let n_windows = windows.len() as f64;

    let avg_oos_sharpe =
        windows.iter().map(|w| w.out_of_sample.sharpe_ratio).sum::<f64>() / n_windows;
    let avg_oos_win_rate =
        windows.iter().map(|w| w.out_of_sample.win_rate_pct).sum::<f64>() / n_windows;
    let total_oos_trades: usize = windows.iter().map(|w| w.out_of_sample.total_trades).sum();
    let avg_oos_return_pct =
        windows.iter().map(|w| w.out_of_sample.total_return_pct).sum::<f64>() / n_windows;

    let profitable = windows
        .iter()
        .filter(|w| w.out_of_sample.sharpe_ratio > 0.0)
        .count() as f64;
    let pct_profitable_windows = profitable / n_windows * 100.0;

    let avg_is_sharpe =
        windows.iter().map(|w| w.in_sample.sharpe_ratio).sum::<f64>() / n_windows;
    let efficiency_ratio = if avg_is_sharpe.abs() > f64::EPSILON {
        avg_oos_sharpe / avg_is_sharpe
    } else {
        0.0
    };

    WalkForwardResult {
        windows,
        avg_oos_sharpe,
        avg_oos_win_rate,
        total_oos_trades,
        avg_oos_return_pct,
        pct_profitable_windows,
        efficiency_ratio,
    }
}

/// Convenience: build a sync engine and run walk-forward in one call.
pub fn walk_forward_sync<S, R>(
    initial_capital: f64,
    strategy: S,
    risk: R,
    commission_pct: f64,
    slippage_pct: f64,
    bars: &[Bar],
    symbol: &str,
    cfg: WalkForwardConfig,
) -> WalkForwardResult
where
    S: Strategy,
    R: RiskManager,
{
    let mut engine = Engine::<S, R, SyncBus>::sync(
        initial_capital,
        strategy,
        risk,
        commission_pct,
        slippage_pct,
    );
    walk_forward(&mut engine, bars, symbol, cfg)
}
