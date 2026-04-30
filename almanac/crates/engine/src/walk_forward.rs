use crate::bus_sync::SyncBus;
use crate::engine::Engine;
use alm_core::{
    bar::Bar,
    bus::EventBus,
    portfolio::EquityPoint,
    strategy::{RiskManager, Strategy},
};
use alm_data::BarVecFeed;
use alm_report::BacktestReport;
use serde::{Deserialize, Serialize};

/// Whether the IS window rolls forward or expands from bar 0.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum WalkForwardMode {
    /// IS window slides forward with the OOS window (default).
    #[default]
    Rolling,
    /// IS window always starts at bar 0 and grows each step (expanding/anchored).
    Anchored,
}

/// Walk-forward validation config.
///
/// Rolling (default):
/// ```text
/// Window 0:  IS=[0..is_bars]             OOS=[is_bars..is_bars+oos_bars]
/// Window 1:  IS=[step..is_bars+step]     OOS=[is_bars+step..is_bars+step+oos_bars]
/// ```
///
/// Anchored:
/// ```text
/// Window 0:  IS=[0..is_bars]             OOS=[is_bars..is_bars+oos_bars]
/// Window 1:  IS=[0..is_bars+step]        OOS=[is_bars+step..is_bars+step+oos_bars]
/// ```
pub struct WalkForwardConfig {
    /// Number of bars in the initial in-sample window.
    pub is_bars: usize,
    /// Minimum bars per out-of-sample window.
    pub oos_bars: usize,
    /// How many bars to advance per step (default = oos_bars → non-overlapping OOS).
    pub step_bars: usize,
    /// Initial capital for each window (engine is fully reset between windows).
    pub initial_capital: f64,
    /// Risk-free annual rate for Sharpe calculation.
    pub risk_free_annual: f64,
    /// Rolling (default) or Anchored (expanding IS from bar 0).
    pub mode: WalkForwardMode,
    /// Bars to skip at the start of each OOS window.
    ///
    /// The strategy warms up its indicators on these bars but no trades count
    /// toward the OOS report. Prevents look-ahead leakage from IS indicator state.
    pub embargo_bars: usize,
    /// Extend the OOS window (in `oos_bars` increments) until at least this many
    /// trades occur. Useful for sparse strategies where a fixed `oos_bars` window
    /// often produces statistically meaningless results.
    pub min_oos_trades: Option<usize>,
}

impl WalkForwardConfig {
    pub fn new(is_bars: usize, oos_bars: usize, initial_capital: f64) -> Self {
        Self {
            is_bars,
            oos_bars,
            step_bars: oos_bars,
            initial_capital,
            risk_free_annual: 0.04,
            mode: WalkForwardMode::default(),
            embargo_bars: 0,
            min_oos_trades: None,
        }
    }
}

/// Single walk-forward window result.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WalkForwardWindow {
    pub window: usize,
    /// Bar index range for in-sample period [start, end).
    pub is_range: (usize, usize),
    /// Bar index range for out-of-sample period [start, end).
    pub oos_range: (usize, usize),
    pub in_sample: BacktestReport,
    pub out_of_sample: BacktestReport,
    /// Raw OOS equity curve for this window (used to stitch the combined curve).
    pub oos_equity_curve: Vec<EquityPoint>,
}

/// Aggregate walk-forward results.
#[derive(Debug, Clone, Serialize, Deserialize)]
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
    /// Combined OOS equity curve — all OOS windows stitched in chronological order.
    /// Represents the "honest" equity curve: how the strategy would have performed
    /// on data it never saw during any training period.
    pub oos_equity_curve: Vec<EquityPoint>,
}

/// Run walk-forward validation on a pre-loaded bar slice.
///
/// The engine is fully reset between windows (capital, strategy state, positions).
/// No parameter optimization is performed — this tests strategy *consistency*.
///
/// # Embargo
/// `embargo_bars` are consumed at the start of each OOS window to let
/// indicators warm up. Trades in the embargo period are excluded from the
/// OOS report; the next window's IS still starts at the normal position.
///
/// # Min OOS trades
/// When `min_oos_trades` is set and the base `oos_bars` window produces fewer
/// trades, the OOS window is extended in `oos_bars` increments until the
/// threshold is met or end-of-data is reached.
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
    let mut window_idx: usize = 0;

    loop {
        // OOS always starts at: is_bars + window_idx * step_bars
        let oos_start = cfg.is_bars + window_idx * cfg.step_bars;
        if oos_start >= n { break; }

        // IS range depends on mode
        let is_start = match cfg.mode {
            WalkForwardMode::Rolling => window_idx * cfg.step_bars,
            WalkForwardMode::Anchored => 0,
        };
        let is_end = oos_start;

        // OOS actual data starts after embargo
        let embargo_end = oos_start + cfg.embargo_bars;
        if embargo_end >= n { break; }

        // Determine OOS end, possibly extending for min_oos_trades
        let mut oos_end = (oos_start + cfg.oos_bars).min(n);

        if let Some(min_trades) = cfg.min_oos_trades {
            // Extend in oos_bars chunks until we have enough trades or hit end-of-data.
            // Each probe fully resets the engine, so results are correct.
            loop {
                if embargo_end >= oos_end { break; }
                engine.reset(cfg.initial_capital);
                let mut probe =
                    BarVecFeed::new(bars[embargo_end..oos_end].to_vec(), symbol.to_string());
                let probe_report = engine.run(&mut probe, cfg.risk_free_annual);
                if probe_report.total_trades >= min_trades || oos_end >= n { break; }
                oos_end = (oos_end + cfg.oos_bars).min(n);
            }
        }

        // ── In-sample ────────────────────────────────────────────────────────
        engine.reset(cfg.initial_capital);
        let mut is_feed =
            BarVecFeed::new(bars[is_start..is_end].to_vec(), symbol.to_string());
        let is_report = engine.run(&mut is_feed, cfg.risk_free_annual);

        // ── Out-of-sample (embargo bars skipped) ─────────────────────────────
        engine.reset(cfg.initial_capital);
        let mut oos_feed =
            BarVecFeed::new(bars[embargo_end..oos_end].to_vec(), symbol.to_string());
        let oos_report = engine.run(&mut oos_feed, cfg.risk_free_annual);

        let oos_equity_curve = engine.portfolio.equity_curve.clone();
        windows.push(WalkForwardWindow {
            window: window_idx,
            is_range: (is_start, is_end),
            oos_range: (embargo_end, oos_end),
            in_sample: is_report,
            out_of_sample: oos_report,
            oos_equity_curve,
        });

        window_idx += 1;

        // Stop when the next window's OOS (after embargo) would have no bars.
        let next_oos_start = cfg.is_bars + window_idx * cfg.step_bars;
        if next_oos_start + cfg.embargo_bars >= n { break; }
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
            oos_equity_curve: vec![],
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

    // Stitch OOS equity curves: scale each window so it starts where the previous ended.
    let oos_equity_curve = stitch_oos_curves(&windows, cfg.initial_capital);

    WalkForwardResult {
        windows,
        avg_oos_sharpe,
        avg_oos_win_rate,
        total_oos_trades,
        avg_oos_return_pct,
        pct_profitable_windows,
        efficiency_ratio,
        oos_equity_curve,
    }
}

/// Stitch per-window OOS equity curves into one continuous curve.
///
/// Each window starts fresh at `initial_capital`, so we chain them by scaling:
/// window[i] equity values are multiplied by (prev_end / initial_capital).
fn stitch_oos_curves(windows: &[WalkForwardWindow], initial_capital: f64) -> Vec<EquityPoint> {
    let mut result: Vec<EquityPoint> = Vec::new();
    let mut scale = 1.0_f64;

    for w in windows {
        let curve = &w.oos_equity_curve;
        if curve.is_empty() {
            continue;
        }
        for pt in curve {
            result.push(EquityPoint {
                timestamp: pt.timestamp,
                equity: pt.equity * scale,
            });
        }
        // Next window scales from where this one ended
        if let Some(last) = curve.last() {
            scale *= last.equity / initial_capital;
        }
    }

    result
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
