//! Monte Carlo simulation for backtest robustness analysis.
//!
//! Bootstraps trade PnL with replacement across `n_iter` iterations.
//! Outputs percentile equity bands and ruin probability.
//!
//! # Method
//! For each iteration:
//!   1. Sample `n_trades` trade returns (with replacement) from the historical list.
//!   2. Simulate a compound equity curve starting from `initial_capital`.
//!   3. Record the final equity and whether the simulation ever hit ruin.
//!
//! Percentile equity *curves* are computed by sorting each time-step across all
//! iterations, giving p5/p25/p50/p75/p95 bands.

use serde::{Deserialize, Serialize};

// ── Config ─────────────────────────────────────────────────────────────────────

/// Configuration for a [`run`] Monte Carlo simulation.
///
/// Use [`Default::default()`] for 1 000 iterations with a 50% ruin threshold and
/// a time-based random seed (non-reproducible).  Set `seed` to a fixed value for
/// deterministic results in tests or benchmarks.
#[derive(Debug, Clone)]
pub struct MonteCarloConfig {
    /// Number of bootstrap iterations (default 1000).
    pub n_iter: usize,
    /// Equity fraction below which a simulation counts as "ruin" (default 0.50 → 50% loss).
    pub ruin_threshold: f64,
    /// Optional RNG seed for reproducibility (None = time-based).
    pub seed: Option<u64>,
}

impl Default for MonteCarloConfig {
    fn default() -> Self {
        Self { n_iter: 1_000, ruin_threshold: 0.50, seed: None }
    }
}

// ── Result ─────────────────────────────────────────────────────────────────────

/// Output of a completed Monte Carlo simulation produced by [`run`].
///
/// Contains scalar percentile final equities (`final_p5` … `final_p95`) for quick
/// summary display, and full fan-chart curves (`curve_p5` … `curve_p95`) of length
/// `n_trades + 1` for plotting.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MonteCarloResult {
    pub n_iter:           usize,
    pub n_trades:         usize,
    pub initial_capital:  f64,
    pub ruin_threshold:   f64,

    /// Fraction of iterations where equity ever dropped below ruin_threshold × initial_capital.
    pub ruin_probability: f64,

    /// Percentile final equities.
    pub final_p5:   f64,
    pub final_p10:  f64,
    pub final_p25:  f64,
    pub final_p50:  f64,
    pub final_p75:  f64,
    pub final_p90:  f64,
    pub final_p95:  f64,

    /// Percentile equity *curves* — each is a Vec of length `n_trades + 1` (including initial).
    /// Bands: p5, p10, p25, p50, p75, p90, p95.
    pub curve_p5:   Vec<f64>,
    pub curve_p10:  Vec<f64>,
    pub curve_p25:  Vec<f64>,
    pub curve_p50:  Vec<f64>,
    pub curve_p75:  Vec<f64>,
    pub curve_p90:  Vec<f64>,
    pub curve_p95:  Vec<f64>,
}

// ── Main entry ─────────────────────────────────────────────────────────────────

/// Run a bootstrap Monte Carlo simulation from historical per-trade returns.
///
/// Resamples `pnl_pct` with replacement `cfg.n_iter` times, each time simulating a
/// compound equity curve of `n_trades` steps from `initial_capital`.
///
/// - `pnl_pct` — per-trade PnL as a fraction (e.g. `0.02` = +2%, `-0.01` = -1%).
///   Use `trade.pnl_pct` from the backtest result. Must have at least 2 elements.
/// - `initial_capital` — starting equity for each simulated path.
/// - `cfg` — controls iteration count, ruin threshold, and optional RNG seed.
///
/// Returns `None` when fewer than 2 trades are provided.
///
/// The `ruin_probability` in the result counts the fraction of paths where equity ever
/// dropped to or below `ruin_threshold * initial_capital` at any step.
/// The percentile curves (`curve_p5` … `curve_p95`) are each `n_trades + 1` long
/// (step 0 = `initial_capital`), giving fan-chart bands for display.
pub fn run(
    pnl_pct: &[f64],
    initial_capital: f64,
    cfg: &MonteCarloConfig,
) -> Option<MonteCarloResult> {
    let n = pnl_pct.len();
    if n < 2 { return None; }

    let seed = cfg.seed.unwrap_or_else(|| {
        // Simple time-based seed (no external dep)
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_nanos() as u64)
            .unwrap_or(42)
    });

    let ruin_floor = initial_capital * cfg.ruin_threshold;
    let mut rng = Xorshift64::new(seed);

    // Collect per-iteration final equities + full equity curves.
    // equity_curves[iter][step]  (step 0 = initial_capital)
    let mut all_finals: Vec<f64> = Vec::with_capacity(cfg.n_iter);
    let mut all_curves: Vec<Vec<f64>> = Vec::with_capacity(cfg.n_iter);
    let mut ruin_count: usize = 0;

    for _ in 0..cfg.n_iter {
        let mut equity = initial_capital;
        let mut curve = Vec::with_capacity(n + 1);
        curve.push(equity);
        let mut ruined = false;

        for _ in 0..n {
            let idx = rng.next_usize(n);
            equity *= 1.0 + pnl_pct[idx];
            curve.push(equity);
            if equity <= ruin_floor { ruined = true; }
        }

        if ruined { ruin_count += 1; }
        all_finals.push(equity);
        all_curves.push(curve);
    }

    // ── Final equity percentiles ───────────────────────────────────────────────
    let mut sorted_finals = all_finals.clone();
    sorted_finals.sort_by(|a, b| a.partial_cmp(b).unwrap_or(std::cmp::Ordering::Equal));

    let final_p5   = percentile_sorted(&sorted_finals, 5.0);
    let final_p10  = percentile_sorted(&sorted_finals, 10.0);
    let final_p25  = percentile_sorted(&sorted_finals, 25.0);
    let final_p50  = percentile_sorted(&sorted_finals, 50.0);
    let final_p75  = percentile_sorted(&sorted_finals, 75.0);
    let final_p90  = percentile_sorted(&sorted_finals, 90.0);
    let final_p95  = percentile_sorted(&sorted_finals, 95.0);

    // ── Per-step percentile curves ─────────────────────────────────────────────
    // For each step t, collect equity[t] across all iterations, then take percentiles.
    let n_steps = n + 1;
    let mut curve_p5   = Vec::with_capacity(n_steps);
    let mut curve_p10  = Vec::with_capacity(n_steps);
    let mut curve_p25  = Vec::with_capacity(n_steps);
    let mut curve_p50  = Vec::with_capacity(n_steps);
    let mut curve_p75  = Vec::with_capacity(n_steps);
    let mut curve_p90  = Vec::with_capacity(n_steps);
    let mut curve_p95  = Vec::with_capacity(n_steps);

    let mut step_vals: Vec<f64> = Vec::with_capacity(cfg.n_iter);
    for t in 0..n_steps {
        step_vals.clear();
        for curve in &all_curves {
            step_vals.push(curve[t]);
        }
        step_vals.sort_by(|a, b| a.partial_cmp(b).unwrap_or(std::cmp::Ordering::Equal));
        curve_p5.push(percentile_sorted(&step_vals, 5.0));
        curve_p10.push(percentile_sorted(&step_vals, 10.0));
        curve_p25.push(percentile_sorted(&step_vals, 25.0));
        curve_p50.push(percentile_sorted(&step_vals, 50.0));
        curve_p75.push(percentile_sorted(&step_vals, 75.0));
        curve_p90.push(percentile_sorted(&step_vals, 90.0));
        curve_p95.push(percentile_sorted(&step_vals, 95.0));
    }

    Some(MonteCarloResult {
        n_iter:          cfg.n_iter,
        n_trades:        n,
        initial_capital,
        ruin_threshold:  cfg.ruin_threshold,
        ruin_probability: ruin_count as f64 / cfg.n_iter as f64,
        final_p5, final_p10, final_p25, final_p50, final_p75, final_p90, final_p95,
        curve_p5, curve_p10, curve_p25, curve_p50, curve_p75, curve_p90, curve_p95,
    })
}

// ── Helpers ────────────────────────────────────────────────────────────────────

/// Linear interpolation percentile on a **pre-sorted** slice.
fn percentile_sorted(sorted: &[f64], pct: f64) -> f64 {
    if sorted.is_empty() { return 0.0; }
    let idx = pct / 100.0 * (sorted.len() - 1) as f64;
    let lo = idx.floor() as usize;
    let hi = (lo + 1).min(sorted.len() - 1);
    let frac = idx - lo as f64;
    sorted[lo] * (1.0 - frac) + sorted[hi] * frac
}

// ── PRNG — xorshift64 ──────────────────────────────────────────────────────────

struct Xorshift64 { state: u64 }

impl Xorshift64 {
    fn new(seed: u64) -> Self {
        Self { state: if seed == 0 { 0xDEAD_BEEF_CAFE_BABEu64 } else { seed } }
    }

    #[inline]
    fn next_u64(&mut self) -> u64 {
        let mut x = self.state;
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
        self.state = x;
        x
    }

    /// Uniform random usize in [0, n)
    #[inline]
    fn next_usize(&mut self, n: usize) -> usize {
        (self.next_u64() % n as u64) as usize
    }
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_percentile_sorted() {
        let v: Vec<f64> = (1..=100).map(|i| i as f64).collect();
        assert!((percentile_sorted(&v, 50.0) - 50.5).abs() < 0.1);
        assert!((percentile_sorted(&v, 0.0) - 1.0).abs() < 0.1);
        assert!((percentile_sorted(&v, 100.0) - 100.0).abs() < 0.1);
    }

    #[test]
    fn test_mc_zero_growth_trades() {
        // All 0% trades → final equity always == initial
        let pnl = vec![0.0f64; 50];
        let result = run(&pnl, 10_000.0, &MonteCarloConfig { n_iter: 100, ..Default::default() })
            .unwrap();
        assert!((result.final_p50 - 10_000.0).abs() < 0.01);
        assert_eq!(result.ruin_probability, 0.0);
    }

    #[test]
    fn test_mc_always_ruin() {
        // Each trade loses 60% → always below 50% threshold quickly
        let pnl = vec![-0.60f64; 5];
        let result = run(&pnl, 10_000.0, &MonteCarloConfig {
            n_iter: 200, ruin_threshold: 0.50, seed: Some(42),
        }).unwrap();
        assert_eq!(result.ruin_probability, 1.0);
    }

    #[test]
    fn test_mc_curve_length() {
        let pnl = vec![0.01f64; 30];
        let result = run(&pnl, 10_000.0, &MonteCarloConfig { n_iter: 50, seed: Some(1), ..Default::default() })
            .unwrap();
        assert_eq!(result.curve_p50.len(), 31); // n+1 steps
        assert!((result.curve_p50[0] - 10_000.0).abs() < 0.01); // starts at initial
    }

    #[test]
    fn test_mc_positive_expectancy() {
        // Mix of +5% / -2% → should be profitable in median
        let pnl: Vec<f64> = (0..40).map(|i| if i % 3 == 0 { 0.05 } else { -0.02 }).collect();
        let result = run(&pnl, 10_000.0, &MonteCarloConfig { n_iter: 500, seed: Some(99), ..Default::default() })
            .unwrap();
        assert!(result.final_p50 > 10_000.0, "median should be profitable");
        assert!(result.final_p5 < result.final_p50);
        assert!(result.final_p95 > result.final_p50);
    }
}
