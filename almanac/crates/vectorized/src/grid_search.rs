//! Parallel parameter grid search for vectorized strategies.
//!
//! Runs all combinations of specified parameter ranges in parallel (rayon)
//! and returns results sorted by a chosen metric.
//!
//! # Example
//! ```ignore
//! use alm_vectorized::grid_search::{GridSearch, SortBy};
//!
//! let results = GridSearch::new("ma_crossover")
//!     .param_range_usize("fast", 5..=20, 5)      // 5, 10, 15, 20
//!     .param_range_usize("slow", 30..=100, 10)    // 30, 40, ..., 100
//!     .initial_capital(10_000.0)
//!     .commission(0.001)
//!     .sort_by(SortBy::Sharpe)
//!     .run(&df, "BTCUSDT")
//!     .unwrap();
//!
//! let best = &results[0];
//! println!("Best params: {:?}, Sharpe: {:.2}", best.params, best.result.sharpe_ratio);
//! ```

use std::collections::HashMap;

use anyhow::{bail, Result};
use polars::prelude::DataFrame;
use rayon::prelude::*;
use serde::{Deserialize, Serialize};

use crate::{
    atr_trailing::backtest_atr_trailing,
    bollinger_breakout::backtest_bollinger_breakout,
    ma_crossover::backtest_ma_crossover,
    macd_crossover::backtest_macd_crossover,
    result::VectorizedResult,
    rsi_mean_rev::backtest_rsi_mean_rev,
    stochastic::backtest_stochastic,
};

// ── Parameter value ────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize)]
pub enum ParamValue {
    Float(f64),
    Int(usize),
}

impl ParamValue {
    pub fn as_f64(&self) -> f64 {
        match self {
            ParamValue::Float(v) => *v,
            ParamValue::Int(v) => *v as f64,
        }
    }

    pub fn as_usize(&self) -> usize {
        match self {
            ParamValue::Float(v) => *v as usize,
            ParamValue::Int(v) => *v,
        }
    }
}

// ── Sort metric ────────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum SortBy {
    #[default]
    Sharpe,
    TotalReturn,
    Sortino,
    /// Lowest max drawdown (ascending)
    MaxDrawdown,
    ProfitFactor,
    WinRate,
}

// ── Grid result ────────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct GridResult {
    pub params: HashMap<String, f64>,
    pub result: VectorizedResult,
}

// ── GridSearch builder ─────────────────────────────────────────────────────────

pub struct GridSearch {
    strategy: String,
    /// name → list of candidate values
    param_values: HashMap<String, Vec<ParamValue>>,
    initial_capital: f64,
    commission_pct: f64,
    sort_by: SortBy,
    top_n: usize,
}

impl GridSearch {
    pub fn new(strategy: impl Into<String>) -> Self {
        Self {
            strategy: strategy.into(),
            param_values: HashMap::new(),
            initial_capital: 10_000.0,
            commission_pct: 0.001,
            sort_by: SortBy::Sharpe,
            top_n: usize::MAX,
        }
    }

    /// Add a range of integer values stepped by `step`.
    ///
    /// `range_usize("fast", 5..=20, 5)` → [5, 10, 15, 20]
    pub fn param_range_usize(
        mut self,
        name: impl Into<String>,
        range: std::ops::RangeInclusive<usize>,
        step: usize,
    ) -> Self {
        let vals: Vec<ParamValue> = (*range.start()..=*range.end())
            .step_by(step)
            .map(ParamValue::Int)
            .collect();
        self.param_values.insert(name.into(), vals);
        self
    }

    /// Add a range of float values stepped by `step`.
    ///
    /// `range_f64("std", 1.5..=3.0, 0.5)` → [1.5, 2.0, 2.5, 3.0]
    pub fn param_range_f64(
        mut self,
        name: impl Into<String>,
        start: f64,
        end: f64,
        step: f64,
    ) -> Self {
        let mut vals = Vec::new();
        let mut v = start;
        while v <= end + f64::EPSILON {
            vals.push(ParamValue::Float((v * 1000.0).round() / 1000.0));
            v += step;
        }
        self.param_values.insert(name.into(), vals);
        self
    }

    /// Add an explicit list of float values.
    pub fn param_list_f64(mut self, name: impl Into<String>, values: Vec<f64>) -> Self {
        self.param_values
            .insert(name.into(), values.into_iter().map(ParamValue::Float).collect());
        self
    }

    /// Add an explicit list of integer values.
    pub fn param_list_usize(mut self, name: impl Into<String>, values: Vec<usize>) -> Self {
        self.param_values
            .insert(name.into(), values.into_iter().map(ParamValue::Int).collect());
        self
    }

    pub fn initial_capital(mut self, capital: f64) -> Self {
        self.initial_capital = capital;
        self
    }

    pub fn commission(mut self, pct: f64) -> Self {
        self.commission_pct = pct;
        self
    }

    pub fn sort_by(mut self, metric: SortBy) -> Self {
        self.sort_by = metric;
        self
    }

    /// Only return the top N results (by chosen metric).
    pub fn top(mut self, n: usize) -> Self {
        self.top_n = n;
        self
    }

    /// Run the grid search. All combinations are evaluated in parallel.
    pub fn run(self, df: &DataFrame, symbol: &str) -> Result<Vec<GridResult>> {
        let combos = cartesian_product(&self.param_values);
        if combos.is_empty() {
            bail!("no parameter combinations to test");
        }

        let strategy = self.strategy.clone();
        let capital = self.initial_capital;
        let commission = self.commission_pct;
        let sort_by = self.sort_by;
        let top_n = self.top_n;

        // Parallel sweep
        let mut results: Vec<GridResult> = combos
            .into_par_iter()
            .filter_map(|params| {
                let vr = run_one(&strategy, &params, df, symbol, capital, commission).ok()?;
                let flat: HashMap<String, f64> =
                    params.iter().map(|(k, v)| (k.clone(), v.as_f64())).collect();
                Some(GridResult { params: flat, result: vr })
            })
            .collect();

        // Sort
        results.sort_by(|a, b| {
            let cmp = match sort_by {
                SortBy::Sharpe => b
                    .result
                    .sharpe_ratio
                    .partial_cmp(&a.result.sharpe_ratio)
                    .unwrap_or(std::cmp::Ordering::Equal),
                SortBy::TotalReturn => b
                    .result
                    .total_return_pct
                    .partial_cmp(&a.result.total_return_pct)
                    .unwrap_or(std::cmp::Ordering::Equal),
                SortBy::Sortino => b
                    .result
                    .sortino_ratio
                    .partial_cmp(&a.result.sortino_ratio)
                    .unwrap_or(std::cmp::Ordering::Equal),
                SortBy::MaxDrawdown => a
                    .result
                    .max_drawdown_pct
                    .partial_cmp(&b.result.max_drawdown_pct)
                    .unwrap_or(std::cmp::Ordering::Equal),
                SortBy::ProfitFactor => b
                    .result
                    .profit_factor
                    .partial_cmp(&a.result.profit_factor)
                    .unwrap_or(std::cmp::Ordering::Equal),
                SortBy::WinRate => b
                    .result
                    .win_rate_pct
                    .partial_cmp(&a.result.win_rate_pct)
                    .unwrap_or(std::cmp::Ordering::Equal),
            };
            cmp
        });

        results.truncate(top_n);
        Ok(results)
    }
}

// ── Strategy dispatch ──────────────────────────────────────────────────────────

fn get_f(p: &HashMap<String, ParamValue>, key: &str, default: f64) -> f64 {
    p.get(key).map(|v| v.as_f64()).unwrap_or(default)
}

fn get_u(p: &HashMap<String, ParamValue>, key: &str, default: usize) -> usize {
    p.get(key).map(|v| v.as_usize()).unwrap_or(default)
}

fn run_one(
    strategy: &str,
    params: &HashMap<String, ParamValue>,
    df: &DataFrame,
    symbol: &str,
    capital: f64,
    commission: f64,
) -> Result<VectorizedResult> {
    match strategy {
        "ma_crossover" => backtest_ma_crossover(
            df,
            symbol,
            get_u(params, "fast", 20),
            get_u(params, "slow", 50),
            capital,
            commission,
        ),
        "rsi_mean_rev" => backtest_rsi_mean_rev(
            df,
            symbol,
            get_u(params, "period", 14),
            get_f(params, "oversold", 30.0),
            get_f(params, "overbought", 70.0),
            capital,
            commission,
        ),
        "macd_crossover" => backtest_macd_crossover(
            df,
            symbol,
            get_u(params, "fast", 12),
            get_u(params, "slow", 26),
            get_u(params, "signal", 9),
            capital,
            commission,
        ),
        "bollinger_breakout" => backtest_bollinger_breakout(
            df,
            symbol,
            get_u(params, "period", 20),
            get_f(params, "num_std", 2.0),
            capital,
            commission,
        ),
        "stochastic" => backtest_stochastic(
            df,
            symbol,
            get_u(params, "k_period", 14),
            get_u(params, "d_period", 3),
            get_f(params, "oversold", 20.0),
            get_f(params, "overbought", 80.0),
            capital,
            commission,
        ),
        "atr_trailing" => backtest_atr_trailing(
            df,
            symbol,
            get_u(params, "ema_period", 20),
            get_u(params, "atr_period", 14),
            get_f(params, "multiplier", 2.0),
            capital,
            commission,
        ),
        other => bail!("grid search: unknown strategy '{other}'"),
    }
}

// ── Cartesian product ──────────────────────────────────────────────────────────

/// Generate all combinations from `{ name → [values] }`.
fn cartesian_product(
    params: &HashMap<String, Vec<ParamValue>>,
) -> Vec<HashMap<String, ParamValue>> {
    if params.is_empty() {
        return vec![HashMap::new()];
    }

    // Deterministic iteration order
    let keys: Vec<&String> = {
        let mut ks: Vec<&String> = params.keys().collect();
        ks.sort();
        ks
    };

    let mut combos: Vec<HashMap<String, ParamValue>> = vec![HashMap::new()];

    for key in keys {
        let values = &params[key];
        let mut new_combos = Vec::with_capacity(combos.len() * values.len());
        for combo in &combos {
            for val in values {
                let mut next = combo.clone();
                next.insert(key.clone(), val.clone());
                new_combos.push(next);
            }
        }
        combos = new_combos;
    }

    combos
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use polars::prelude::*;

    fn make_df(prices: Vec<f64>) -> DataFrame {
        let n = prices.len();
        DataFrame::new(vec![
            Column::new("t".into(), (0..n as i64).collect::<Vec<i64>>()),
            Column::new("o".into(), prices.clone()),
            Column::new("h".into(), prices.iter().map(|&p| p + 1.0).collect::<Vec<_>>()),
            Column::new("l".into(), prices.iter().map(|&p| p - 1.0).collect::<Vec<_>>()),
            Column::new("c".into(), prices.clone()),
            Column::new("v".into(), vec![1000i64; n]),
        ])
        .unwrap()
    }

    #[test]
    fn test_grid_search_ma_crossover() {
        let prices: Vec<f64> = (0..200).map(|i| 100.0 + i as f64 * 0.5).collect();
        let df = make_df(prices);

        let results = GridSearch::new("ma_crossover")
            .param_range_usize("fast", 5..=15, 5)
            .param_range_usize("slow", 20..=40, 10)
            .initial_capital(10_000.0)
            .commission(0.001)
            .sort_by(SortBy::Sharpe)
            .top(5)
            .run(&df, "TEST")
            .unwrap();

        assert!(!results.is_empty(), "should have results");
        // fast must be < slow — our combos include invalid ones; run_one handles them gracefully
        for r in &results {
            assert!(r.result.equity_curve.len() == 200);
        }
    }

    #[test]
    fn test_cartesian_product() {
        let mut params = HashMap::new();
        params.insert("a".into(), vec![ParamValue::Int(1), ParamValue::Int(2)]);
        params.insert("b".into(), vec![ParamValue::Int(10), ParamValue::Int(20)]);

        let combos = cartesian_product(&params);
        assert_eq!(combos.len(), 4);
    }

    #[test]
    fn test_grid_search_rsi() {
        let mut prices = Vec::new();
        for cycle in 0..10 {
            for i in 0..20 {
                let base = 100.0 + cycle as f64 * 2.0;
                prices.push(base + 10.0 * (i as f64 * std::f64::consts::PI / 10.0).sin());
            }
        }
        let df = make_df(prices);

        let results = GridSearch::new("rsi_mean_rev")
            .param_range_usize("period", 10..=20, 5)
            .param_list_f64("oversold", vec![25.0, 30.0])
            .param_list_f64("overbought", vec![70.0, 75.0])
            .sort_by(SortBy::TotalReturn)
            .run(&df, "TEST")
            .unwrap();

        assert!(!results.is_empty());
    }
}
