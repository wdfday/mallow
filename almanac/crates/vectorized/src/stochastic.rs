use crate::result::VectorizedResult;
use crate::utils;
use anyhow::{Context, Result};
use polars::prelude::*;

/// Vectorized Stochastic Oscillator backtest.
///
/// Logic:
///   Buy  when %K crosses above `oversold` from below.
///   Sell when %K crosses above `overbought`.
pub fn backtest_stochastic(
    df: &DataFrame,
    symbol: &str,
    k_period: usize,
    d_period: usize,
    oversold: f64,
    overbought: f64,
    initial_capital: f64,
    commission_pct: f64,
) -> Result<VectorizedResult> {
    let close = df.column("c").context("missing 'c' column")?.as_materialized_series();
    let high = df.column("h").context("missing 'h' column")?.as_materialized_series();
    let low = df.column("l").context("missing 'l' column")?.as_materialized_series();

    let k_series = utils::stoch_k_series(high, low, close, k_period);
    // %D = SMA of %K over d_period
    let d_series = utils::sma_series(&k_series, d_period);

    let k_vals = k_series.f64()?;
    let d_vals = d_series.f64()?;
    let close_vals = close.f64()?;
    let n = close_vals.len();

    let mut positions = vec![0.0f64; n];
    let mut in_pos = false;

    for i in 1..n {
        let k = k_vals.get(i - 1).unwrap_or(f64::NAN);
        let d = d_vals.get(i - 1).unwrap_or(f64::NAN);

        if k.is_nan() || d.is_nan() {
            positions[i] = if in_pos { 1.0 } else { 0.0 };
            continue;
        }

        if !in_pos && k < oversold && k > d {
            in_pos = true;
        } else if in_pos && k > overbought {
            in_pos = false;
        }

        positions[i] = if in_pos { 1.0 } else { 0.0 };
    }

    let close_prices: Vec<f64> = (0..n).map(|i| close_vals.get(i).unwrap_or(0.0)).collect();
    let (equity_curve, pnl_trades) =
        utils::simulate_equity(&close_prices, &positions, initial_capital, commission_pct);

    let name = format!("vec_stochastic({k_period},{d_period},{oversold},{overbought})");
    Ok(VectorizedResult::compute(&name, symbol, initial_capital, equity_curve, &pnl_trades))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_stochastic_basic() {
        let mut prices = Vec::new();
        for cycle in 0..6 {
            for i in 0..20 {
                let base = 100.0 + cycle as f64 * 3.0;
                prices.push(base + 15.0 * (i as f64 * std::f64::consts::PI / 10.0).sin());
            }
        }
        let n = prices.len();
        let df = DataFrame::new(vec![
            Column::new("t".into(), (0..n as i64).collect::<Vec<i64>>()),
            Column::new("o".into(), prices.clone()),
            Column::new("h".into(), prices.iter().map(|&p| p + 2.0).collect::<Vec<_>>()),
            Column::new("l".into(), prices.iter().map(|&p| p - 2.0).collect::<Vec<_>>()),
            Column::new("c".into(), prices.clone()),
            Column::new("v".into(), vec![1000i64; n]),
        ])
        .unwrap();

        let result =
            backtest_stochastic(&df, "TEST", 14, 3, 20.0, 80.0, 100_000.0, 0.001).unwrap();
        assert_eq!(result.equity_curve.len(), n);
    }
}
