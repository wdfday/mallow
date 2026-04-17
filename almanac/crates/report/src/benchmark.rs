use crate::metrics;
use serde::{Deserialize, Serialize};

/// Buy-and-hold benchmark: buy at first bar's open, hold until last bar's close.
/// Computed directly from the close-price series — no portfolio/commission needed.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BuyHoldBenchmark {
    pub total_return_pct: f64,
    pub cagr_pct: f64,
    pub annualized_volatility_pct: f64,
    pub sharpe_ratio: f64,
    pub sortino_ratio: f64,
    pub max_drawdown_pct: f64,
    pub max_dd_duration_bars: usize,
}

impl BuyHoldBenchmark {
    /// Compute B&H stats from a slice of close prices + timestamps (Unix ms).
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
