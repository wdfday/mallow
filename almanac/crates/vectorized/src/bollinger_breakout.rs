use crate::result::VectorizedResult;
use crate::utils;
use anyhow::{Context, Result};
use polars::prelude::*;

/// Vectorized Bollinger Band Breakout backtest.
///
/// Logic:
///   Long  when close crosses above the upper band.
///   Close when close crosses below the middle (SMA) line.
pub fn backtest_bollinger_breakout(
    df: &DataFrame,
    symbol: &str,
    period: usize,
    num_std: f64,
    initial_capital: f64,
    commission_pct: f64,
) -> Result<VectorizedResult> {
    let close = df.column("c").context("missing 'c' column")?.as_materialized_series();
    let (upper, middle, _lower) = utils::bb_series(close, period, num_std);

    let close_vals = close.f64()?;
    let upper_vals = upper.f64()?;
    let mid_vals = middle.f64()?;
    let n = close_vals.len();

    let mut positions = vec![0.0f64; n];
    let mut in_pos = false;

    for i in 1..n {
        let c = close_vals.get(i - 1).unwrap_or(f64::NAN);
        let u = upper_vals.get(i - 1).unwrap_or(f64::NAN);
        let m = mid_vals.get(i - 1).unwrap_or(f64::NAN);

        if c.is_nan() || u.is_nan() || m.is_nan() {
            positions[i] = if in_pos { 1.0 } else { 0.0 };
            continue;
        }

        if !in_pos && c > u {
            in_pos = true;
        } else if in_pos && c < m {
            in_pos = false;
        }

        positions[i] = if in_pos { 1.0 } else { 0.0 };
    }

    let close_prices: Vec<f64> = (0..n).map(|i| close_vals.get(i).unwrap_or(0.0)).collect();
    let (equity_curve, pnl_trades) =
        utils::simulate_equity(&close_prices, &positions, initial_capital, commission_pct);

    let name = format!("vec_bollinger_breakout({period},{num_std})");
    Ok(VectorizedResult::compute(&name, symbol, initial_capital, equity_curve, &pnl_trades))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_bollinger_breakout_basic() {
        let mut prices: Vec<f64> = (0..60).map(|i| 100.0 + (i as f64 * 0.5)).collect();
        prices.extend(vec![130.0, 132.0, 135.0, 133.0, 128.0, 125.0, 120.0]);
        let n = prices.len();
        let df = DataFrame::new(vec![
            Column::new("t".into(), (0..n as i64).collect::<Vec<i64>>()),
            Column::new("o".into(), prices.clone()),
            Column::new("h".into(), prices.iter().map(|&p| p + 1.0).collect::<Vec<_>>()),
            Column::new("l".into(), prices.iter().map(|&p| p - 1.0).collect::<Vec<_>>()),
            Column::new("c".into(), prices.clone()),
            Column::new("v".into(), vec![1000i64; n]),
        ])
        .unwrap();

        let result =
            backtest_bollinger_breakout(&df, "TEST", 20, 2.0, 100_000.0, 0.001).unwrap();
        assert_eq!(result.equity_curve.len(), n);
    }
}
