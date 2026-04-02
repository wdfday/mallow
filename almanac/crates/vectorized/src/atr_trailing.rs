use crate::result::VectorizedResult;
use crate::utils;
use anyhow::{Context, Result};
use polars::prelude::*;

/// Vectorized ATR Trailing Stop backtest.
///
/// Logic:
///   Long  when close > EMA(ema_period).
///   Trail stop at close - multiplier * ATR(atr_period).
///   Exit  when close falls below the trailing stop.
pub fn backtest_atr_trailing(
    df: &DataFrame,
    symbol: &str,
    ema_period: usize,
    atr_period: usize,
    multiplier: f64,
    initial_capital: f64,
    commission_pct: f64,
) -> Result<VectorizedResult> {
    let close = df.column("c").context("missing 'c' column")?.as_materialized_series();
    let high = df.column("h").context("missing 'h' column")?.as_materialized_series();
    let low = df.column("l").context("missing 'l' column")?.as_materialized_series();

    let ema = utils::ema_series(close, ema_period);
    let atr = utils::atr_series(high, low, close, atr_period);

    let close_vals = close.f64()?;
    let ema_vals = ema.f64()?;
    let atr_vals = atr.f64()?;
    let n = close_vals.len();

    let mut positions = vec![0.0f64; n];
    let mut in_pos = false;
    let mut trail_stop = 0.0f64;

    for i in 1..n {
        let c = close_vals.get(i - 1).unwrap_or(f64::NAN);
        let e = ema_vals.get(i - 1).unwrap_or(f64::NAN);
        let a = atr_vals.get(i - 1).unwrap_or(f64::NAN);

        if c.is_nan() || e.is_nan() || a.is_nan() {
            positions[i] = if in_pos { 1.0 } else { 0.0 };
            continue;
        }

        let stop = c - multiplier * a;

        if !in_pos && c > e {
            in_pos = true;
            trail_stop = stop;
        } else if in_pos {
            // Ratchet stop up (never down)
            trail_stop = trail_stop.max(stop);
            if c < trail_stop {
                in_pos = false;
            }
        }

        positions[i] = if in_pos { 1.0 } else { 0.0 };
    }

    let close_prices: Vec<f64> = (0..n).map(|i| close_vals.get(i).unwrap_or(0.0)).collect();
    let (equity_curve, pnl_trades) =
        utils::simulate_equity(&close_prices, &positions, initial_capital, commission_pct);

    let name = format!("vec_atr_trailing({ema_period},{atr_period},{multiplier})");
    Ok(VectorizedResult::compute(&name, symbol, initial_capital, equity_curve, &pnl_trades))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_atr_trailing_basic() {
        let mut prices: Vec<f64> = (1..=80).map(|i| 100.0 + i as f64 * 0.8).collect();
        prices.extend((0..40).map(|i| 164.0 - i as f64 * 1.2));
        let n = prices.len();
        let df = DataFrame::new(vec![
            Column::new("t".into(), (0..n as i64).collect::<Vec<i64>>()),
            Column::new("o".into(), prices.clone()),
            Column::new("h".into(), prices.iter().map(|&p| p + 1.5).collect::<Vec<_>>()),
            Column::new("l".into(), prices.iter().map(|&p| p - 1.5).collect::<Vec<_>>()),
            Column::new("c".into(), prices.clone()),
            Column::new("v".into(), vec![1000i64; n]),
        ])
        .unwrap();

        let result =
            backtest_atr_trailing(&df, "TEST", 20, 14, 2.0, 100_000.0, 0.001).unwrap();
        assert_eq!(result.equity_curve.len(), n);
    }
}
