use crate::result::VectorizedResult;
use crate::utils;
use anyhow::{Context, Result};
use polars::prelude::*;

/// Vectorized MA Crossover backtest.
///
/// Operates on the entire DataFrame at once — no bar-by-bar loop.
/// ~10-50x faster than event-driven for parameter sweeps.
///
/// Logic:
///   position = 1 when fast_ema > slow_ema (long)
///   position = 0 when fast_ema <= slow_ema (flat)
pub fn backtest_ma_crossover(
    df: &DataFrame,
    symbol: &str,
    fast_period: usize,
    slow_period: usize,
    initial_capital: f64,
    commission_pct: f64,
) -> Result<VectorizedResult> {
    let close_col = df.column("c").context("missing 'c' column")?;
    let close = close_col.as_materialized_series();

    // Compute EMAs
    let fast_ema = utils::ema_series(close, fast_period);
    let slow_ema = utils::ema_series(close, slow_period);

    let fast_vals = fast_ema.f64()?;
    let slow_vals = slow_ema.f64()?;
    let close_vals = close.f64()?;
    let n = close_vals.len();

    // Generate position signal: 1.0 when fast > slow, else 0.0
    // Shift by 1 to avoid look-ahead bias (trade on next bar)
    let mut positions = vec![0.0f64; n];
    #[allow(clippy::needless_range_loop)]
    for i in 1..n {
        let f = fast_vals.get(i - 1).unwrap_or(f64::NAN);
        let s = slow_vals.get(i - 1).unwrap_or(f64::NAN);
        if f.is_nan() || s.is_nan() {
            positions[i] = 0.0;
        } else if f > s {
            positions[i] = 1.0;
        }
    }

    // Extract close prices
    let close_prices: Vec<f64> = (0..n).map(|i| close_vals.get(i).unwrap_or(0.0)).collect();

    // Simulate equity
    let (equity_curve, pnl_trades) =
        utils::simulate_equity(&close_prices, &positions, initial_capital, commission_pct);

    let name = format!("vec_ma_crossover({fast_period},{slow_period})");
    Ok(VectorizedResult::compute(
        &name,
        symbol,
        initial_capital,
        equity_curve,
        &pnl_trades,
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_ma_crossover_basic() {
        // Create uptrend then downtrend → should enter and exit
        let mut prices: Vec<f64> = (1..=60).map(|i| 100.0 + i as f64).collect();
        prices.extend((1..=40).map(|i| 160.0 - i as f64 * 1.5));
        let n = prices.len();

        let df = DataFrame::new(vec![
            Column::new("t".into(), (0..n as i64).collect::<Vec<i64>>()),
            Column::new("o".into(), prices.clone()),
            Column::new("h".into(), prices.clone()),
            Column::new("l".into(), prices.clone()),
            Column::new("c".into(), prices.clone()),
            Column::new("v".into(), vec![1000i64; n]),
        ])
        .unwrap();

        let result = backtest_ma_crossover(&df, "TEST", 5, 20, 100_000.0, 0.001).unwrap();
        assert!(
            result.total_trades > 0,
            "should have at least 1 completed trade"
        );
        assert_eq!(result.equity_curve.len(), n);
    }
}
