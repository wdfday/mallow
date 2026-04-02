use crate::result::VectorizedResult;
use crate::utils;
use anyhow::{Context, Result};
use polars::prelude::*;

/// Vectorized MACD Crossover backtest.
///
/// Logic:
///   Long  when MACD histogram crosses above 0 (MACD > signal).
///   Close when MACD histogram crosses below 0.
pub fn backtest_macd_crossover(
    df: &DataFrame,
    symbol: &str,
    fast: usize,
    slow: usize,
    signal: usize,
    initial_capital: f64,
    commission_pct: f64,
) -> Result<VectorizedResult> {
    let close = df.column("c").context("missing 'c' column")?.as_materialized_series();
    let (_macd, _sig, hist) = utils::macd_series(close, fast, slow, signal);

    let hist_vals = hist.f64()?;
    let close_vals = close.f64()?;
    let n = close_vals.len();

    let mut positions = vec![0.0f64; n];
    let mut in_pos = false;

    for i in 1..n {
        let h = hist_vals.get(i - 1).unwrap_or(f64::NAN);
        let h_prev = if i >= 2 { hist_vals.get(i - 2).unwrap_or(f64::NAN) } else { f64::NAN };

        if h.is_nan() || h_prev.is_nan() {
            positions[i] = if in_pos { 1.0 } else { 0.0 };
            continue;
        }

        if !in_pos && h_prev <= 0.0 && h > 0.0 {
            in_pos = true;
        } else if in_pos && h_prev >= 0.0 && h < 0.0 {
            in_pos = false;
        }

        positions[i] = if in_pos { 1.0 } else { 0.0 };
    }

    let close_prices: Vec<f64> = (0..n).map(|i| close_vals.get(i).unwrap_or(0.0)).collect();
    let (equity_curve, pnl_trades) =
        utils::simulate_equity(&close_prices, &positions, initial_capital, commission_pct);

    let name = format!("vec_macd_crossover({fast},{slow},{signal})");
    Ok(VectorizedResult::compute(&name, symbol, initial_capital, equity_curve, &pnl_trades))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_macd_crossover_basic() {
        let mut prices: Vec<f64> = (1..=80).map(|i| 100.0 + i as f64).collect();
        prices.extend((1..=40).map(|i| 180.0 - i as f64 * 1.5));
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

        let result = backtest_macd_crossover(&df, "TEST", 12, 26, 9, 100_000.0, 0.001).unwrap();
        assert_eq!(result.equity_curve.len(), n);
    }
}
