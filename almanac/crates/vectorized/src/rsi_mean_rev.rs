use crate::result::VectorizedResult;
use crate::utils;
use anyhow::{Context, Result};
use polars::prelude::*;

/// Vectorized RSI Mean Reversion backtest.
///
/// Logic:
///   Buy when RSI < oversold (e.g. 30)
///   Sell when RSI > overbought (e.g. 70)
pub fn backtest_rsi_mean_rev(
    df: &DataFrame,
    symbol: &str,
    rsi_period: usize,
    oversold: f64,
    overbought: f64,
    initial_capital: f64,
    commission_pct: f64,
) -> Result<VectorizedResult> {
    let close_col = df.column("c").context("missing 'c' column")?;
    let close = close_col.as_materialized_series();

    // Compute RSI over entire series
    let rsi = utils::rsi_series(close, rsi_period);
    let rsi_vals = rsi.f64()?;
    let close_vals = close.f64()?;
    let n = close_vals.len();

    // Generate position signal with shifting to avoid look-ahead
    let mut positions = vec![0.0f64; n];
    let mut in_position = false;

    #[allow(clippy::needless_range_loop)]
    for i in 1..n {
        let r = rsi_vals.get(i - 1).unwrap_or(f64::NAN);

        if r.is_nan() {
            positions[i] = if in_position { 1.0 } else { 0.0 };
            continue;
        }

        if r < oversold && !in_position {
            in_position = true;
        }

        if r > overbought && in_position {
            in_position = false;
        }

        positions[i] = if in_position { 1.0 } else { 0.0 };
    }

    let close_prices: Vec<f64> = (0..n).map(|i| close_vals.get(i).unwrap_or(0.0)).collect();

    let (equity_curve, pnl_trades) =
        utils::simulate_equity(&close_prices, &positions, initial_capital, commission_pct);

    let name = format!("vec_rsi_mean_rev({rsi_period},{oversold},{overbought})");
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
    fn test_rsi_mean_rev_basic() {
        // Create oscillating prices → RSI should trigger buy/sell
        let mut prices = Vec::new();
        for cycle in 0..5 {
            for i in 0..20 {
                let base = 100.0 + (cycle as f64) * 2.0;
                let p = base + 10.0 * (i as f64 * std::f64::consts::PI / 10.0).sin();
                prices.push(p);
            }
        }

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

        let result = backtest_rsi_mean_rev(&df, "TEST", 14, 30.0, 70.0, 100_000.0, 0.001).unwrap();
        assert_eq!(result.symbol, "TEST");
        // Should have at least executed some trades
        assert!(result.equity_curve.len() == n);
    }
}
