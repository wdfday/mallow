use crate::metrics;
use serde::{Deserialize, Serialize};

/// Buy-and-hold benchmark: buy at the first bar's close price and hold to the last.
///
/// Computed directly from a close-price series — no commission, no position sizing.
/// Compare strategy metrics against this to gauge whether the strategy adds value
/// over passive exposure to the underlying asset.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BuyHoldBenchmark {
    /// Simple total return `(last_close - first_close) / first_close × 100`.
    pub total_return_pct: f64,
    /// Compound Annual Growth Rate of the buy-and-hold position.
    pub cagr_pct: f64,
    /// Annualised standard deviation of bar returns × 100.
    pub annualized_volatility_pct: f64,
    /// Sharpe ratio of the buy-and-hold position (using the same risk-free rate as the strategy).
    pub sharpe_ratio: f64,
    /// Sortino ratio of the buy-and-hold position.
    pub sortino_ratio: f64,
    /// Largest peak-to-trough decline in the buy-and-hold equity curve (percentage points).
    pub max_drawdown_pct: f64,
    /// Bars from the equity peak to the trough of the worst buy-and-hold drawdown.
    pub max_dd_duration_bars: usize,
}

impl BuyHoldBenchmark {
    /// Compute buy-and-hold statistics from a close-price series.
    ///
    /// - `closes` — bar close prices in chronological order (at least 2 required).
    /// - `timestamps` — corresponding Unix millisecond timestamps; must be the same length as `closes`.
    /// - `risk_free_annual` — annualised risk-free rate as a fraction (e.g. `0.05` = 5%),
    ///   used for Sharpe and Sortino calculations.
    ///
    /// The annualization factor is derived empirically from timestamps so that the result
    /// is correct for both daily-bar (US stocks) and minute-bar (crypto) inputs.
    ///
    /// Returns a zeroed struct when fewer than 2 prices are provided.
    pub fn compute(closes: &[f64], timestamps: &[i64], risk_free_annual: f64) -> Self {
        assert_eq!(closes.len(), timestamps.len());
        let n = closes.len();
        if n < 2 {
            return Self::zero();
        }

        let first = closes[0];
        let last = closes[n - 1];
        let total_return = (last - first) / first * 100.0;

        let duration_years =
            (timestamps[n - 1] - timestamps[0]) as f64 / (365.25 * 24.0 * 3600.0 * 1000.0);
        let cagr = if duration_years > 0.0 {
            ((last / first).powf(1.0 / duration_years) - 1.0) * 100.0
        } else {
            0.0
        };

        // Equity curve = initial * (close / first_close) at each bar
        let equity: Vec<f64> = closes.iter().map(|c| c / first).collect();
        let bar_returns = metrics::bar_returns(&equity);

        // Annualization factor: empirical bars-per-year from timestamps
        let bars_per_year = if duration_years > 1e-9 {
            (n - 1) as f64 / duration_years
        } else {
            252.0
        };

        let ann_vol = metrics::std_dev(&bar_returns) * bars_per_year.sqrt() * 100.0;

        let rf_per_bar = (1.0 + risk_free_annual).powf(1.0 / bars_per_year) - 1.0;
        let sharpe  = metrics::sharpe_ratio(&bar_returns, rf_per_bar, bars_per_year);
        let sortino = metrics::sortino_ratio(&bar_returns, rf_per_bar, bars_per_year);

        let (max_dd, max_dd_bars, _) = metrics::drawdown_stats(&equity);

        Self {
            total_return_pct: total_return,
            cagr_pct: cagr,
            annualized_volatility_pct: ann_vol,
            sharpe_ratio: sharpe,
            sortino_ratio: sortino,
            max_drawdown_pct: max_dd * 100.0,
            max_dd_duration_bars: max_dd_bars,
        }
    }

    fn zero() -> Self {
        Self {
            total_return_pct: 0.0,
            cagr_pct: 0.0,
            annualized_volatility_pct: 0.0,
            sharpe_ratio: 0.0,
            sortino_ratio: 0.0,
            max_drawdown_pct: 0.0,
            max_dd_duration_bars: 0,
        }
    }
}
