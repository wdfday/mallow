use alm_core::{order::Side, trade::Trade};
use serde::{Deserialize, Serialize};

/// Compute bar-to-bar returns from an equity curve.
///
/// Each element is `(equity[i+1] - equity[i]) / equity[i]`.
/// Depending on the bar frequency these may be 1-minute, hourly, or daily
/// returns — use `ann_factor = bars_per_year` when annualizing.
pub fn bar_returns(equity: &[f64]) -> Vec<f64> {
    equity.windows(2).map(|w| (w[1] - w[0]) / w[0]).collect()
}

/// Alias kept for backwards-compat within this crate; prefer `bar_returns`.
#[inline]
pub fn daily_returns(equity: &[f64]) -> Vec<f64> {
    bar_returns(equity)
}

/// Arithmetic mean of a slice.
///
/// Returns `0.0` when the slice is empty.
pub fn mean(v: &[f64]) -> f64 {
    if v.is_empty() {
        return 0.0;
    }
    v.iter().sum::<f64>() / v.len() as f64
}

/// Bessel-corrected (sample) standard deviation.
///
/// Formula: `sqrt(sum((x - mean)^2) / (n - 1))`.
/// Returns `0.0` when fewer than 2 elements are present.
pub fn std_dev(v: &[f64]) -> f64 {
    if v.len() < 2 {
        return 0.0;
    }
    let m = mean(v);
    let var = v.iter().map(|x| (x - m).powi(2)).sum::<f64>() / (v.len() - 1) as f64;
    var.sqrt()
}

/// Sharpe ratio.
///
/// `ann_factor` = bars_per_year (e.g. 525_960 for M1 crypto, 252 for daily stocks).
/// Use `BacktestReport`'s auto-computed value rather than a hardcoded constant.
pub fn sharpe_ratio(bar_returns: &[f64], risk_free_per_bar: f64, ann_factor: f64) -> f64 {
    let n = bar_returns.len();
    if n < 2 {
        return 0.0;
    }
    let excess: Vec<f64> = bar_returns.iter().map(|r| r - risk_free_per_bar).collect();
    let m = mean(&excess);
    let s = std_dev(&excess);
    if s < f64::EPSILON {
        return 0.0;
    }
    m / s * ann_factor.sqrt()
}

/// Sortino ratio (standard definition: downside deviation over **all** periods).
///
/// `ann_factor` = bars_per_year.
pub fn sortino_ratio(bar_returns: &[f64], risk_free_per_bar: f64, ann_factor: f64) -> f64 {
    let n = bar_returns.len();
    if n < 2 {
        return 0.0;
    }
    let excess: Vec<f64> = bar_returns.iter().map(|r| r - risk_free_per_bar).collect();
    let m = mean(&excess);
    // Standard Sortino: divide by n (all periods), not just negative periods.
    let downside_sq_sum: f64 = excess.iter().map(|&e| if e < 0.0 { e * e } else { 0.0 }).sum();
    if downside_sq_sum < f64::EPSILON {
        return f64::INFINITY;
    }
    let downside_dev = (downside_sq_sum / n as f64).sqrt();
    if downside_dev < f64::EPSILON {
        return 0.0;
    }
    m / downside_dev * ann_factor.sqrt()
}

/// Compute Sharpe and Sortino ratios in a single pass over `bar_returns`.
///
/// - `ann_factor` = bars_per_year — use the value auto-computed from the equity
///   curve timestamps in `BacktestReport::generate()`, not a hardcoded constant.
/// - Sortino uses the **standard** denominator (all n periods), not just
///   the count of negative periods.
pub fn sharpe_sortino(bar_returns: &[f64], risk_free_per_bar: f64, ann_factor: f64) -> (f64, f64) {
    let n = bar_returns.len();
    if n < 2 {
        return (0.0, 0.0);
    }

    // Pass 1: mean of excess returns.
    let excess_mean =
        bar_returns.iter().map(|r| r - risk_free_per_bar).sum::<f64>() / n as f64;

    // Pass 2: variance of excess + downside sum simultaneously.
    let mut sum_sq_dev    = 0.0_f64;
    let mut downside_sq   = 0.0_f64;
    let mut has_downside  = false;
    for &r in bar_returns {
        let e   = r - risk_free_per_bar;
        let dev = e - excess_mean;
        sum_sq_dev += dev * dev;
        if e < 0.0 {
            downside_sq  += e * e;
            has_downside  = true;
        }
    }

    let sqrt_ann = ann_factor.sqrt();
    let std = (sum_sq_dev / (n - 1) as f64).sqrt();
    let sharpe = if std < f64::EPSILON { 0.0 } else { excess_mean / std * sqrt_ann };

    // Standard Sortino: downside deviation = sqrt(mean of squared negatives over ALL n).
    let sortino = if !has_downside {
        f64::INFINITY
    } else {
        let downside_dev = (downside_sq / n as f64).sqrt();
        if downside_dev < f64::EPSILON { 0.0 } else { excess_mean / downside_dev * sqrt_ann }
    };

    (sharpe, sortino)
}

/// Maximum drawdown, its duration in bars, and the average drawdown over the equity curve.
///
/// Iterates once over `equity`, tracking the running peak.
/// Returns `(max_drawdown_frac, max_dd_bars, avg_drawdown_frac)` where all drawdown
/// values are fractional (e.g. `0.20` = 20% loss from peak).
/// `max_dd_bars` is the number of bars from peak to the deepest trough.
/// Returns `(0.0, 0, 0.0)` for an empty slice.
pub fn drawdown_stats(equity: &[f64]) -> (f64, usize, f64) {
    if equity.is_empty() {
        return (0.0, 0, 0.0);
    }

    let mut peak = equity[0];
    let mut max_dd = 0.0f64;
    let mut max_dd_bars = 0usize;
    let mut dd_start = 0usize;
    let mut dd_sum = 0.0;
    let mut dd_count = 0usize;

    for (i, &eq) in equity.iter().enumerate() {
        if eq > peak {
            peak = eq;
            dd_start = i;
        }
        let dd = (peak - eq) / peak;
        if dd > max_dd {
            max_dd = dd;
            max_dd_bars = i - dd_start;
        }
        if dd > 0.0 {
            dd_sum += dd;
            dd_count += 1;
        }
    }

    let avg_dd = if dd_count > 0 {
        dd_sum / dd_count as f64
    } else {
        0.0
    };
    (max_dd, max_dd_bars, avg_dd)
}

/// Core trade statistics computed in a single pass over closed trades.
///
/// Returns `(win_rate, profit_factor, expectancy, avg_win_pct, avg_loss_pct)`:
/// - `win_rate` — fraction of winning trades `[0, 1]`
/// - `profit_factor` — `gross_profit / gross_loss` (currency); `INFINITY` when there are no losses, `0.0` when no trades
/// - `expectancy` — `win_rate * avg_win_pct - loss_rate * avg_loss_pct` (both as fractions)
/// - `avg_win_pct` / `avg_loss_pct` — mean `pnl_pct` of winners / losers (loss returned as positive)
///
/// Edge cases: empty slice → `(0.0, 0.0, 0.0, 0.0, 0.0)`. All wins → `profit_factor = INFINITY`.
pub fn trade_stats(trades: &[Trade]) -> (f64, f64, f64, f64, f64) {
    if trades.is_empty() {
        return (0.0, 0.0, 0.0, 0.0, 0.0);
    }

    let wins: Vec<&Trade> = trades.iter().filter(|t| t.is_winner()).collect();
    let losses: Vec<&Trade> = trades.iter().filter(|t| !t.is_winner()).collect();

    let win_rate = wins.len() as f64 / trades.len() as f64;

    let gross_profit: f64 = wins.iter().map(|t| t.pnl).sum();
    let gross_loss: f64 = losses.iter().map(|t| t.pnl.abs()).sum();
    let profit_factor = if gross_loss > f64::EPSILON {
        gross_profit / gross_loss
    } else if gross_profit > 0.0 {
        f64::INFINITY
    } else {
        0.0
    };

    let avg_win = if wins.is_empty() {
        0.0
    } else {
        wins.iter().map(|t| t.pnl_pct).sum::<f64>() / wins.len() as f64
    };
    let avg_loss = if losses.is_empty() {
        0.0
    } else {
        losses.iter().map(|t| t.pnl_pct.abs()).sum::<f64>() / losses.len() as f64
    };

    let loss_rate = 1.0 - win_rate;
    let expectancy = win_rate * avg_win - loss_rate * avg_loss;

    (win_rate, profit_factor, expectancy, avg_win, avg_loss)
}

/// Longest unbroken streak of losing trades in sequence.
///
/// Scans trades in order; a win resets the counter.
/// Returns `0` when there are no trades or no losing trades.
pub fn max_consecutive_losses(trades: &[Trade]) -> usize {
    let mut max = 0usize;
    let mut current = 0usize;
    for t in trades {
        if t.is_winner() {
            current = 0;
        } else {
            current += 1;
            max = max.max(current);
        }
    }
    max
}

/// Longest unbroken streak of winning trades in sequence.
///
/// Scans trades in order; a loss resets the counter.
/// Returns `0` when there are no trades or no winning trades.
pub fn max_consecutive_wins(trades: &[Trade]) -> usize {
    let mut max = 0usize;
    let mut current = 0usize;
    for t in trades {
        if t.is_winner() {
            current += 1;
            max = max.max(current);
        } else {
            current = 0;
        }
    }
    max
}

/// Largest single win and largest single loss (both as pnl_pct, loss returned as positive).
pub fn largest_win_loss(trades: &[Trade]) -> (f64, f64) {
    let largest_win = trades.iter().filter(|t| t.is_winner())
        .map(|t| t.pnl_pct)
        .fold(0.0_f64, f64::max);
    let largest_loss = trades.iter().filter(|t| !t.is_winner())
        .map(|t| t.pnl_pct.abs())
        .fold(0.0_f64, f64::max);
    (largest_win, largest_loss)
}

// ── Long / Short split ────────────────────────────────────────────────────────

/// Per-direction trade statistics.
///
/// Produced by [`direction_stats`]. All `_pct` fields are raw fractions, not percentage points
/// (e.g. `0.05` = 5%). `avg_loss_pct` is stored as a positive value.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct DirectionStats {
    /// Total number of trades in this direction.
    pub count: usize,
    /// Fraction of trades that were winners `[0, 1]`.
    pub win_rate: f64,
    /// Gross profit / gross loss; `INFINITY` when no losses; `0.0` when no trades.
    pub profit_factor: f64,
    /// Mean `pnl_pct` of winning trades (positive fraction).
    pub avg_win_pct: f64,
    /// Mean absolute `pnl_pct` of losing trades (positive fraction).
    pub avg_loss_pct: f64,
    /// `win_rate * avg_win_pct - loss_rate * avg_loss_pct`.
    pub expectancy: f64,
}

/// Split trade stats by side (long = Buy, short = Sell).
pub fn direction_stats(trades: &[Trade]) -> (DirectionStats, DirectionStats) {
    let longs:  Vec<&Trade> = trades.iter().filter(|t| t.side == Side::Buy).collect();
    let shorts: Vec<&Trade> = trades.iter().filter(|t| t.side == Side::Sell).collect();
    (side_stats(&longs), side_stats(&shorts))
}

fn side_stats(trades: &[&Trade]) -> DirectionStats {
    if trades.is_empty() {
        return DirectionStats::default();
    }
    let wins:   Vec<&&Trade> = trades.iter().filter(|t| t.is_winner()).collect();
    let losses: Vec<&&Trade> = trades.iter().filter(|t| !t.is_winner()).collect();
    let win_rate = wins.len() as f64 / trades.len() as f64;

    let gross_profit: f64 = wins.iter().map(|t| t.pnl).sum();
    let gross_loss:   f64 = losses.iter().map(|t| t.pnl.abs()).sum();
    let profit_factor = if gross_loss > f64::EPSILON {
        gross_profit / gross_loss
    } else if gross_profit > 0.0 {
        f64::INFINITY
    } else {
        0.0
    };

    let avg_win = if wins.is_empty() { 0.0 } else {
        wins.iter().map(|t| t.pnl_pct).sum::<f64>() / wins.len() as f64
    };
    let avg_loss = if losses.is_empty() { 0.0 } else {
        losses.iter().map(|t| t.pnl_pct.abs()).sum::<f64>() / losses.len() as f64
    };
    let expectancy = win_rate * avg_win - (1.0 - win_rate) * avg_loss;

    DirectionStats {
        count: trades.len(),
        win_rate,
        profit_factor,
        avg_win_pct: avg_win,
        avg_loss_pct: avg_loss,
        expectancy,
    }
}

// ── SQN ───────────────────────────────────────────────────────────────────────

/// Van Tharp's System Quality Number.
///
/// Uses `pnl_pct` as the R-multiple proxy. Score guide:
/// < 1.6 = poor, 1.6–1.9 = below average, 2.0–2.4 = average,
/// 2.5–2.9 = good, 3.0–5.0 = excellent, > 5.0 = holy-grail territory.
///
/// n is capped at 100 per Van Tharp's original definition to avoid SQN
/// inflating arbitrarily with more trades.
pub fn sqn(trades: &[Trade]) -> f64 {
    if trades.len() < 2 {
        return 0.0;
    }
    let r: Vec<f64> = trades.iter().map(|t| t.pnl_pct).collect();
    let m = mean(&r);
    let s = std_dev(&r);
    if s < f64::EPSILON {
        return 0.0;
    }
    let n = (trades.len() as f64).min(100.0);
    n.sqrt() * m / s
}

// ── Monthly returns ───────────────────────────────────────────────────────────

/// Monthly return breakdown derived from the equity curve.
///
/// Each entry is `(year, month_1_to_12, return_pct)`.
/// Uses the last equity point of each calendar month.
pub fn monthly_returns(equity_curve: &[(i64, f64)]) -> Vec<(i32, u32, f64)> {
    if equity_curve.len() < 2 {
        return vec![];
    }

    // Key: (year, month) → last equity of that month.
    let mut months: std::collections::BTreeMap<(i32, u32), f64> =
        std::collections::BTreeMap::new();

    for &(ts_ms, equity) in equity_curve {
        let (year, month) = ts_to_year_month(ts_ms);
        months.insert((year, month), equity);
    }

    let entries: Vec<((i32, u32), f64)> = months.into_iter().collect();
    let mut result = Vec::with_capacity(entries.len());

    for i in 1..entries.len() {
        let ((year, month), eq_end) = entries[i];
        let (_, eq_start) = entries[i - 1];
        if eq_start.abs() > f64::EPSILON {
            let ret = (eq_end - eq_start) / eq_start * 100.0;
            result.push((year, month, ret));
        }
    }

    result
}

fn ts_to_year_month(ts_ms: i64) -> (i32, u32) {
    // Days since Unix epoch
    let days = ts_ms / 86_400_000;
    // Rata Die algorithm (no chrono dep)
    let z = days + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146_096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let month = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    let year = if month <= 2 { y + 1 } else { y } as i32;
    (year, month)
}

// ── PSR / DSR ─────────────────────────────────────────────────────────────────

/// Sample skewness of a return series (moment-based, per-observation normalisation).
///
/// Formula: `(1/n) * sum(((x - mean) / std)^3)`.
/// Positive = right tail; negative = left tail (fat loss tail, common in leveraged strategies).
/// Returns `0.0` when fewer than 3 elements or zero variance.
pub fn skewness(v: &[f64]) -> f64 {
    if v.len() < 3 { return 0.0; }
    let m = mean(v);
    let s = std_dev(v);
    if s < f64::EPSILON { return 0.0; }
    let n = v.len() as f64;
    v.iter().map(|x| ((x - m) / s).powi(3)).sum::<f64>() / n
}

/// Sample excess kurtosis (total kurtosis minus 3) of a return series.
///
/// Formula: `(1/n) * sum(((x - mean) / std)^4) - 3`.
/// Normal distribution → `0`. Positive = heavier tails than normal (leptokurtic).
/// Returns `0.0` when fewer than 4 elements or zero variance.
pub fn excess_kurtosis(v: &[f64]) -> f64 {
    if v.len() < 4 { return 0.0; }
    let m = mean(v);
    let s = std_dev(v);
    if s < f64::EPSILON { return 0.0; }
    let n = v.len() as f64;
    v.iter().map(|x| ((x - m) / s).powi(4)).sum::<f64>() / n - 3.0
}

/// Probabilistic Sharpe Ratio — P(true SR > `benchmark_sr`) given the observed
/// return series (López de Prado, 2012).
///
/// Pass **per-bar** (non-annualised) returns. `benchmark_sr` must be in the
/// same units (per-bar). Use `0.0` to test whether SR is significantly positive.
///
/// Accounts for non-normality via the skewness and kurtosis of the returns.
pub fn psr(returns: &[f64], benchmark_sr: f64) -> f64 {
    let t = returns.len();
    if t < 4 { return 0.0; }
    let m = mean(returns);
    let s = std_dev(returns);
    if s < f64::EPSILON { return 0.0; }
    let sr_hat  = m / s;
    let gamma1  = skewness(returns);
    let gamma2  = excess_kurtosis(returns) + 3.0; // total kurtosis
    let var_sr  = (1.0 - gamma1 * sr_hat + (gamma2 - 1.0) / 4.0 * sr_hat * sr_hat)
                  / (t as f64 - 1.0);
    if var_sr <= 0.0 { return 0.0; }
    normal_cdf((sr_hat - benchmark_sr) / var_sr.sqrt())
}

/// Deflated Sharpe Ratio — PSR corrected for the multiple-testing bias of
/// running `n_trials` parameter combinations (López de Prado, 2014).
///
/// Pass the return series of the **best** strategy found and the total number
/// of parameter sets evaluated. Returns the probability that the winner is
/// genuinely skilled rather than the lucky result of a large search.
pub fn dsr(returns: &[f64], n_trials: usize) -> f64 {
    if n_trials <= 1 || returns.len() < 4 { return psr(returns, 0.0); }
    let t  = returns.len() as f64;
    let m  = mean(returns);
    let s  = std_dev(returns);
    if s < f64::EPSILON { return 0.0; }
    let sr_hat = m / s;
    let gamma1 = skewness(returns);
    let gamma2 = excess_kurtosis(returns) + 3.0;
    let var_sr = (1.0 - gamma1 * sr_hat + (gamma2 - 1.0) / 4.0 * sr_hat * sr_hat)
                 / (t - 1.0);
    if var_sr <= 0.0 { return 0.0; }
    // Expected max SR under H₀ (no skill) over n_trials — Euler–Mascheroni formula
    const GAMMA_EM: f64 = 0.577_215_664_9;
    let n = n_trials as f64;
    let e_max = var_sr.sqrt()
        * ((1.0 - GAMMA_EM) * probit(1.0 - 1.0 / n)
           + GAMMA_EM       * probit(1.0 - 1.0 / (n * std::f64::consts::E)));
    psr(returns, e_max)
}

/// Standard normal CDF Φ(x). Abramowitz & Stegun 26.2.17, max error ≈ 7.5 × 10⁻⁸.
pub fn normal_cdf(x: f64) -> f64 {
    const P:  f64 = 0.231_641_9;
    const B: [f64; 5] = [
         0.319_381_530,
        -0.356_563_782,
         1.781_477_937,
        -1.821_255_978,
         1.330_274_429,
    ];
    let t   = 1.0 / (1.0 + P * x.abs());
    let pdf = (-0.5 * x * x).exp() / (2.0 * std::f64::consts::PI).sqrt();
    let q   = pdf * t * (B[0] + t * (B[1] + t * (B[2] + t * (B[3] + t * B[4]))));
    if x >= 0.0 { 1.0 - q } else { q }
}

/// Inverse standard normal CDF (probit). Peter Acklam's algorithm, max error ≈ 1.15 × 10⁻⁹.
pub fn probit(p: f64) -> f64 {
    if p <= 0.0 { return f64::NEG_INFINITY; }
    if p >= 1.0 { return f64::INFINITY; }
    const P_LOW: f64 = 0.02425;
    const A: [f64; 6] = [
        -3.969_683_028_665_376e+01,  2.209_460_984_245_205e+02,
        -2.759_285_104_469_687e+02,  1.383_577_518_672_690e+02,
        -3.066_479_806_614_716e+01,  2.506_628_277_459_239e+00,
    ];
    const B: [f64; 5] = [
        -5.447_609_879_822_406e+01,  1.615_858_368_580_409e+02,
        -1.556_989_798_598_866e+02,  6.680_131_188_771_972e+01,
        -1.328_068_155_288_572e+01,
    ];
    const C: [f64; 6] = [
        -7.784_894_002_430_293e-03, -3.223_964_580_411_365e-01,
        -2.400_758_277_161_838e+00, -2.549_732_539_343_734e+00,
         4.374_664_141_464_968e+00,  2.938_163_982_698_783e+00,
    ];
    const D: [f64; 4] = [
         7.784_695_709_041_462e-03,  3.224_671_290_700_398e-01,
         2.445_134_137_142_996e+00,  3.754_408_661_907_416e+00,
    ];
    if p < P_LOW {
        let q = (-2.0 * p.ln()).sqrt();
        (((((C[0]*q+C[1])*q+C[2])*q+C[3])*q+C[4])*q+C[5])
            / ((((D[0]*q+D[1])*q+D[2])*q+D[3])*q+1.0)
    } else if p <= 1.0 - P_LOW {
        let q = p - 0.5;
        let r = q * q;
        (((((A[0]*r+A[1])*r+A[2])*r+A[3])*r+A[4])*r+A[5])*q
            / (((((B[0]*r+B[1])*r+B[2])*r+B[3])*r+B[4])*r+1.0)
    } else {
        let q = (-2.0 * (1.0 - p).ln()).sqrt();
        -((((( C[0]*q+C[1])*q+C[2])*q+C[3])*q+C[4])*q+C[5])
             / ((((D[0]*q+D[1])*q+D[2])*q+D[3])*q+1.0)
    }
}

// ── Rolling metrics ───────────────────────────────────────────────────────────

/// Rolling Sharpe ratio over a sliding window of equity-curve returns.
/// Returns a Vec with the same length as `equity` (zero-padded for the warm-up).
///
/// `ann_factor` = bars_per_year (same value used in `sharpe_sortino`).
pub fn rolling_sharpe(equity: &[f64], window: usize, ann_factor: f64) -> Vec<f64> {
    if equity.len() < 2 {
        return vec![0.0; equity.len()];
    }
    let returns = bar_returns(equity);
    let mut result = vec![0.0_f64; equity.len()];
    let sqrt_ann = ann_factor.sqrt();
    for i in 0..returns.len() {
        if i + 1 < window {
            result[i + 1] = 0.0;
            continue;
        }
        let slice = &returns[(i + 1 - window)..=i];
        let m = mean(slice);
        let s = std_dev(slice);
        result[i + 1] = if s < f64::EPSILON { 0.0 } else { m / s * sqrt_ann };
    }
    result
}

/// Rolling drawdown percentage at each bar.
/// Returns a Vec with the same length as `equity`.
pub fn rolling_drawdown(equity: &[f64]) -> Vec<f64> {
    if equity.is_empty() {
        return vec![];
    }
    let mut peak = equity[0];
    equity
        .iter()
        .map(|&eq| {
            if eq > peak {
                peak = eq;
            }
            (peak - eq) / peak * 100.0
        })
        .collect()
}

// ── Advanced scalar risk metrics ──────────────────────────────────────────────

/// Value at Risk (95%) and Conditional VaR / Expected Shortfall (CVaR/ES) from bar returns.
///
/// Sorts the return series and reads the empirical 5th percentile for VaR.
/// CVaR is the mean of the worst 5% tail.
/// Returns `(var_95, cvar_95)` as positive fractions (e.g. `0.03` = 3% loss).
/// Both values are clamped to `>= 0`; returns `(0.0, 0.0)` for an empty input.
pub fn var_cvar_95(daily_returns: &[f64]) -> (f64, f64) {
    if daily_returns.is_empty() {
        return (0.0, 0.0);
    }
    let mut sorted = daily_returns.to_vec();
    sorted.sort_by(|a, b| a.partial_cmp(b).unwrap_or(std::cmp::Ordering::Equal));
    let n = sorted.len();
    // VaR 95%: the 5th percentile (worst 5%)
    let var_idx = ((n as f64) * 0.05).ceil() as usize;
    let var_idx = var_idx.min(n).saturating_sub(1);
    let var_95 = -sorted[var_idx]; // report as positive loss

    // CVaR 95%: mean of the worst 5%
    let tail_end = var_idx + 1; // inclusive end for tail slice
    let tail = &sorted[..tail_end];
    let cvar_95 = if tail.is_empty() {
        var_95
    } else {
        -mean(tail) // mean of negative returns → positive
    };

    (var_95.max(0.0), cvar_95.max(0.0))
}

/// Omega ratio: probability-weighted gains divided by probability-weighted losses relative to `threshold`.
///
/// Formula: `sum(max(r - threshold, 0)) / sum(max(threshold - r, 0))` over all bar returns.
/// `threshold = 0.0` evaluates absolute profitability; values above `1.0` indicate a net-positive distribution.
/// Returns `INFINITY` when there are no below-threshold returns; `0.0` for an empty input.
pub fn omega_ratio(daily_returns: &[f64], threshold: f64) -> f64 {
    if daily_returns.is_empty() {
        return 0.0;
    }
    let gains: f64 = daily_returns.iter().map(|&r| (r - threshold).max(0.0)).sum();
    let losses: f64 = daily_returns.iter().map(|&r| (threshold - r).max(0.0)).sum();
    if losses < f64::EPSILON {
        if gains > 0.0 { f64::INFINITY } else { 0.0 }
    } else {
        gains / losses
    }
}

/// Tail ratio: magnitude of right-tail gains relative to left-tail losses.
///
/// Formula: `|p95 return| / |p5 return|` over the sorted return series.
/// Values above `1.0` mean the best days are larger than the worst days.
/// Requires at least 20 data points; returns `0.0` otherwise or when `p5 ≈ 0`.
pub fn tail_ratio(daily_returns: &[f64]) -> f64 {
    if daily_returns.len() < 20 {
        return 0.0;
    }
    let mut sorted = daily_returns.to_vec();
    sorted.sort_by(|a, b| a.partial_cmp(b).unwrap_or(std::cmp::Ordering::Equal));
    let n = sorted.len();
    let p95_idx = ((n as f64) * 0.95) as usize;
    let p5_idx = ((n as f64) * 0.05) as usize;
    let p95 = sorted[p95_idx.min(n - 1)].abs();
    let p5 = sorted[p5_idx].abs();
    if p5 < f64::EPSILON { 0.0 } else { p95 / p5 }
}

/// Recovery factor: total return earned per unit of maximum drawdown risk taken.
///
/// Formula: `total_return / max_drawdown` where both are raw fractions (not percentage points).
/// Higher is better. Returns `0.0` when `max_drawdown ≈ 0` (no drawdown ever occurred).
pub fn recovery_factor(total_return: f64, max_drawdown: f64) -> f64 {
    if max_drawdown.abs() < f64::EPSILON {
        0.0
    } else {
        total_return / max_drawdown.abs()
    }
}

/// Ulcer Index — RMS of drawdown depths (expressed in %).
///
/// Captures both depth *and* duration of underwater periods. Lower is better.
/// UI = sqrt(sum(DD_i^2) / n) where DD_i is the pct drawdown at each equity point.
pub fn ulcer_index(equity: &[f64]) -> f64 {
    let n = equity.len();
    if n < 2 {
        return 0.0;
    }
    let mut peak = equity[0];
    let sum_sq: f64 = equity.iter().map(|&e| {
        if e > peak { peak = e; }
        let dd = if peak > f64::EPSILON { (peak - e) / peak * 100.0 } else { 0.0 };
        dd * dd
    }).sum();
    (sum_sq / n as f64).sqrt()
}

/// Serenity Ratio: CAGR relative to drawdown pain (Ulcer Index).
///
/// Formula: `cagr_pct / ulcer_index`. Higher is better; analogous to Sharpe but
/// uses UI instead of standard deviation, making it more sensitive to prolonged drawdowns.
/// Returns `0.0` when `ulcer_index ≈ 0` (equity never draws down).
pub fn serenity_ratio(cagr_pct: f64, ui: f64) -> f64 {
    if ui < f64::EPSILON { 0.0 } else { cagr_pct / ui }
}

/// Kelly Criterion: optimal fraction of capital to risk per trade.
///
/// Formula: `k = W - (1 - W) / R`
/// where `W` = win_rate `[0, 1]` and `R` = `avg_win_pct / avg_loss_pct` (both positive).
/// Result is clamped to `[-1, 1]`. Negative `k` means the edge is against you.
/// In practice, use half-Kelly (`k / 2`) to reduce variance.
/// Returns `0.0` when either `avg_win_pct` or `avg_loss_pct` is zero.
pub fn kelly_pct(win_rate: f64, avg_win_pct: f64, avg_loss_pct: f64) -> f64 {
    if avg_loss_pct.abs() < f64::EPSILON || avg_win_pct.abs() < f64::EPSILON {
        return 0.0;
    }
    let r = avg_win_pct.abs() / avg_loss_pct.abs();
    (win_rate - (1.0 - win_rate) / r).clamp(-1.0, 1.0)
}

/// Average trade frequency expressed as trades per calendar year.
///
/// Formula: `total_trades / duration_years`.
/// Returns `0.0` when `duration_years ≈ 0`.
pub fn trades_per_year(total_trades: usize, duration_years: f64) -> f64 {
    if duration_years < f64::EPSILON { 0.0 } else { total_trades as f64 / duration_years }
}

/// Payoff ratio: average winning return divided by average losing return.
///
/// Formula: `|avg_win_pct| / |avg_loss_pct|`. Both inputs should be positive.
/// A ratio above `1.0` means winners are larger on average than losers.
/// Returns `0.0` when `avg_loss_pct ≈ 0` (no losing trades).
pub fn payoff_ratio(avg_win_pct: f64, avg_loss_pct: f64) -> f64 {
    if avg_loss_pct.abs() < f64::EPSILON { 0.0 } else { avg_win_pct.abs() / avg_loss_pct.abs() }
}

/// Minimum win rate needed to break even given a payoff ratio.
///
/// Formula: `1 / (1 + payoff_ratio) * 100` (result in percent, e.g. `33.3` means 33.3%).
/// Use this alongside the actual win rate to judge how much margin of safety exists.
/// Returns `0.0` when `payoff_ratio ≈ 0`.
pub fn breakeven_win_rate(payoff_ratio: f64) -> f64 {
    if payoff_ratio < f64::EPSILON { 0.0 } else { 1.0 / (1.0 + payoff_ratio) * 100.0 }
}

/// Total currency profit from all winning trades and total currency loss from all losing trades.
///
/// Returns `(gross_profit, gross_loss)` where `gross_loss` is expressed as a positive number.
/// Both values are based on `trade.pnl` (not `pnl_pct`), so they are in the same currency unit
/// as the backtest's initial capital.
pub fn gross_profit_loss_usd(trades: &[Trade]) -> (f64, f64) {
    let gross_profit: f64 = trades.iter().filter(|t| t.is_winner()).map(|t| t.pnl).sum();
    let gross_loss: f64 = trades.iter().filter(|t| !t.is_winner()).map(|t| t.pnl.abs()).sum();
    (gross_profit, gross_loss)
}

/// Average number of bars held for winning trades vs losing trades.
///
/// Returns `(avg_winners, avg_losers)`.
/// Useful for detecting whether the strategy cuts losses quickly and lets winners run.
/// Returns `0.0` for a group when no trades of that outcome exist.
pub fn avg_bars_held_by_outcome(trades: &[Trade]) -> (f64, f64) {
    let winners: Vec<_> = trades.iter().filter(|t| t.is_winner()).collect();
    let losers: Vec<_> = trades.iter().filter(|t| !t.is_winner()).collect();
    let avg = |v: &[&Trade]| {
        if v.is_empty() { 0.0 } else { v.iter().map(|t| t.bars_held as f64).sum::<f64>() / v.len() as f64 }
    };
    (avg(&winners), avg(&losers))
}

/// MFE capture ratio: for winning trades, average of `pnl_pct / mfe_pct`.
/// Measures how much of the maximum potential gain was actually captured (0–1).
/// A value of 0.7 means we typically exit at 70% of the peak move.
pub fn mfe_capture_ratio(trades: &[Trade]) -> f64 {
    let winners: Vec<_> = trades.iter().filter(|t| t.is_winner() && t.mfe_pct > f64::EPSILON).collect();
    if winners.is_empty() { return 0.0; }
    let sum: f64 = winners.iter().map(|t| (t.pnl_pct / t.mfe_pct).min(1.0).max(0.0)).sum();
    sum / winners.len() as f64
}

/// Compound monthly returns into annual returns.
///
/// Input: `monthly` — slice of `(year, month 1-12, return_pct)` as produced by [`monthly_returns`].
/// Output: `(year, annual_return_pct)` sorted ascending by year.
/// Compounding: each month's return is applied multiplicatively: `product((1 + r/100)) - 1`.
/// Partial years (first/last calendar year) are included as-is.
pub fn yearly_returns(monthly: &[(i32, u32, f64)]) -> Vec<(i32, f64)> {
    use std::collections::BTreeMap;
    let mut by_year: BTreeMap<i32, f64> = BTreeMap::new();
    for &(year, _, ret_pct) in monthly {
        let e = by_year.entry(year).or_insert(1.0);
        *e *= 1.0 + ret_pct / 100.0;
    }
    by_year.into_iter().map(|(y, v)| (y, (v - 1.0) * 100.0)).collect()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::order::Side;

    fn make_trade(pnl: f64, pnl_pct: f64) -> Trade {
        Trade {
            symbol: "T".into(),
            side: Side::Buy,
            qty: 1.0,
            entry_price: 100.0,
            exit_price: 100.0 + pnl,
            entry_timestamp: 0,
            exit_timestamp: 3_600_000,
            pnl,
            pnl_pct,
            commission: 0.0,
            mae_pct: 0.0,
            mfe_pct: 0.0,
            bars_held: 0,
            exit_reason: alm_core::exit::ExitReason::Signal,
        }
    }

    // ── daily_returns ──────────────────────────────────────────────────────────

    #[test]
    fn daily_returns_empty() {
        assert!(daily_returns(&[]).is_empty());
    }

    #[test]
    fn daily_returns_single() {
        assert!(daily_returns(&[100.0]).is_empty());
    }

    #[test]
    fn daily_returns_flat() {
        let r = daily_returns(&[100.0, 100.0, 100.0]);
        assert!(r.iter().all(|&x| x == 0.0));
    }

    #[test]
    fn daily_returns_values() {
        let r = daily_returns(&[100.0, 110.0, 99.0]);
        assert!((r[0] - 0.1).abs() < 1e-10);          // +10%
        assert!((r[1] - (-0.1)).abs() < 1e-10);        // -10%
    }

    // ── mean / std_dev ─────────────────────────────────────────────────────────

    #[test]
    fn mean_empty() {
        assert_eq!(mean(&[]), 0.0);
    }

    #[test]
    fn mean_values() {
        assert!((mean(&[1.0, 2.0, 3.0]) - 2.0).abs() < 1e-10);
    }

    #[test]
    fn std_dev_empty() {
        assert_eq!(std_dev(&[]), 0.0);
    }

    #[test]
    fn std_dev_single() {
        assert_eq!(std_dev(&[5.0]), 0.0);
    }

    #[test]
    fn std_dev_known() {
        // [1, 2, 3]: mean=2, sum_sq_dev=2, sample_var=1, std=1.0
        let v = [1.0, 2.0, 3.0];
        assert!((std_dev(&v) - 1.0).abs() < 1e-10);
    }

    // ── sharpe_ratio ───────────────────────────────────────────────────────────

    #[test]
    fn sharpe_empty() {
        assert_eq!(sharpe_ratio(&[], 0.0, 252.0), 0.0);
    }

    #[test]
    fn sharpe_flat_returns() {
        // All returns equal rf → excess always 0 → sharpe = 0
        let r = vec![0.001; 252];
        assert_eq!(sharpe_ratio(&r, 0.001, 252.0), 0.0);
    }

    #[test]
    fn sharpe_positive() {
        // Alternating +0.01 / +0.005 → mean > 0, std > 0 → Sharpe > 0
        let r: Vec<f64> = (0..252).map(|i| if i % 2 == 0 { 0.01 } else { 0.005 }).collect();
        assert!(sharpe_ratio(&r, 0.0, 252.0) > 0.0);
    }

    #[test]
    fn sharpe_negative() {
        // Alternating -0.01 / -0.005 → mean < 0 → Sharpe < 0
        let r: Vec<f64> = (0..252).map(|i| if i % 2 == 0 { -0.01 } else { -0.005 }).collect();
        assert!(sharpe_ratio(&r, 0.0, 252.0) < 0.0);
    }

    // ── sortino_ratio ──────────────────────────────────────────────────────────

    #[test]
    fn sortino_no_downside() {
        // Only positive returns → sortino = +inf
        let r = vec![0.005; 100];
        assert_eq!(sortino_ratio(&r, 0.0, 252.0), f64::INFINITY);
    }

    #[test]
    fn sortino_positive() {
        let mut r = vec![0.003; 100];
        r.extend(vec![-0.001; 20]);
        assert!(sortino_ratio(&r, 0.0, 252.0) > 0.0);
    }

    // ── drawdown_stats ─────────────────────────────────────────────────────────

    #[test]
    fn drawdown_empty() {
        assert_eq!(drawdown_stats(&[]), (0.0, 0, 0.0));
    }

    #[test]
    fn drawdown_monotone_up() {
        let equity: Vec<f64> = (1..=10).map(|i| 100.0 * i as f64).collect();
        let (max_dd, _, avg_dd) = drawdown_stats(&equity);
        assert_eq!(max_dd, 0.0);
        assert_eq!(avg_dd, 0.0);
    }

    #[test]
    fn drawdown_known() {
        // 100 → 150 → 90: drawdown from 150 = (150-90)/150 = 40%
        let equity = vec![100.0, 150.0, 90.0];
        let (max_dd, bars, _) = drawdown_stats(&equity);
        assert!((max_dd - 0.4).abs() < 1e-10);
        assert_eq!(bars, 1);
    }

    #[test]
    fn drawdown_recovery() {
        // 100 → 80 → 120 → 100: max dd = 20% at bar 1
        let equity = vec![100.0, 80.0, 120.0, 100.0];
        let (max_dd, _, _) = drawdown_stats(&equity);
        assert!((max_dd - 0.2).abs() < 1e-10);
    }

    // ── trade_stats ────────────────────────────────────────────────────────────

    #[test]
    fn trade_stats_empty() {
        let (wr, pf, exp, aw, al) = trade_stats(&[]);
        assert_eq!((wr, pf, exp, aw, al), (0.0, 0.0, 0.0, 0.0, 0.0));
    }

    #[test]
    fn trade_stats_all_wins() {
        let trades = vec![make_trade(10.0, 0.10), make_trade(5.0, 0.05)];
        let (wr, pf, _, _, al) = trade_stats(&trades);
        assert_eq!(wr, 1.0);
        assert_eq!(pf, f64::INFINITY);
        assert_eq!(al, 0.0);
    }

    #[test]
    fn trade_stats_mixed() {
        // 2 wins (+10%, +10%), 1 loss (-5%)
        let trades = vec![
            make_trade(10.0, 0.10),
            make_trade(10.0, 0.10),
            make_trade(-5.0, -0.05),
        ];
        let (wr, pf, exp, avg_win, avg_loss) = trade_stats(&trades);
        assert!((wr - 2.0 / 3.0).abs() < 1e-10);
        assert!((pf - 20.0 / 5.0).abs() < 1e-10);   // gross_profit/gross_loss = 20/5 = 4
        assert!((avg_win - 0.10).abs() < 1e-10);
        assert!((avg_loss - 0.05).abs() < 1e-10);
        // expectancy = 2/3*0.10 - 1/3*0.05
        let expected_exp = 2.0 / 3.0 * 0.10 - 1.0 / 3.0 * 0.05;
        assert!((exp - expected_exp).abs() < 1e-10);
    }

    #[test]
    fn trade_stats_all_losses() {
        let trades = vec![make_trade(-10.0, -0.10), make_trade(-5.0, -0.05)];
        let (wr, pf, _, _, _) = trade_stats(&trades);
        assert_eq!(wr, 0.0);
        assert_eq!(pf, 0.0);
    }

    // ── max_consecutive_losses ─────────────────────────────────────────────────

    #[test]
    fn consec_losses_none() {
        let trades = vec![make_trade(5.0, 0.05); 5];
        assert_eq!(max_consecutive_losses(&trades), 0);
    }

    #[test]
    fn consec_losses_streak() {
        let trades = vec![
            make_trade(5.0, 0.05),
            make_trade(-1.0, -0.01),
            make_trade(-1.0, -0.01),
            make_trade(-1.0, -0.01),
            make_trade(5.0, 0.05),
            make_trade(-1.0, -0.01),
            make_trade(-1.0, -0.01),
        ];
        assert_eq!(max_consecutive_losses(&trades), 3);
    }

    #[test]
    fn consec_losses_all() {
        let trades = vec![make_trade(-1.0, -0.01); 7];
        assert_eq!(max_consecutive_losses(&trades), 7);
    }

    fn make_trade_side(pnl: f64, pnl_pct: f64, side: Side) -> Trade {
        Trade { side, ..make_trade(pnl, pnl_pct) }
    }

    // ── max_consecutive_wins ───────────────────────────────────────────────────

    #[test]
    fn consec_wins_streak() {
        let trades = vec![
            make_trade(5.0, 0.05),
            make_trade(5.0, 0.05),
            make_trade(5.0, 0.05),
            make_trade(-1.0, -0.01),
            make_trade(5.0, 0.05),
            make_trade(5.0, 0.05),
        ];
        assert_eq!(max_consecutive_wins(&trades), 3);
    }

    // ── largest_win_loss ───────────────────────────────────────────────────────

    #[test]
    fn largest_win_loss_mixed() {
        let trades = vec![
            make_trade(10.0, 0.10),
            make_trade(3.0, 0.03),
            make_trade(-7.0, -0.07),
            make_trade(-2.0, -0.02),
        ];
        let (w, l) = largest_win_loss(&trades);
        assert!((w - 0.10).abs() < 1e-10);
        assert!((l - 0.07).abs() < 1e-10);
    }

    #[test]
    fn largest_win_loss_all_wins() {
        let trades = vec![make_trade(5.0, 0.05), make_trade(10.0, 0.10)];
        let (_, l) = largest_win_loss(&trades);
        assert_eq!(l, 0.0);
    }

    // ── direction_stats ────────────────────────────────────────────────────────

    #[test]
    fn direction_stats_split() {
        let trades = vec![
            make_trade_side(10.0,  0.10, Side::Buy),
            make_trade_side(10.0,  0.10, Side::Buy),
            make_trade_side(-5.0, -0.05, Side::Buy),
            make_trade_side(8.0,   0.08, Side::Sell),
            make_trade_side(-3.0, -0.03, Side::Sell),
        ];
        let (l, s) = direction_stats(&trades);
        assert_eq!(l.count, 3);
        assert_eq!(s.count, 2);
        assert!((l.win_rate - 2.0 / 3.0).abs() < 1e-10);
        assert!((s.win_rate - 0.5).abs() < 1e-10);
    }

    #[test]
    fn direction_stats_empty_short() {
        let trades = vec![make_trade_side(5.0, 0.05, Side::Buy)];
        let (l, s) = direction_stats(&trades);
        assert_eq!(l.count, 1);
        assert_eq!(s.count, 0);
        assert_eq!(s.win_rate, 0.0);
    }

    // ── sqn ────────────────────────────────────────────────────────────────────

    #[test]
    fn sqn_empty() {
        assert_eq!(sqn(&[]), 0.0);
    }

    #[test]
    fn sqn_positive_expectancy() {
        // 80% win rate with +5% avg win, -2% avg loss → positive SQN
        let trades: Vec<Trade> = (0..50)
            .map(|i| if i < 40 { make_trade(5.0, 0.05) } else { make_trade(-2.0, -0.02) })
            .collect();
        assert!(sqn(&trades) > 0.0);
    }

    #[test]
    fn sqn_negative_expectancy() {
        let trades: Vec<Trade> = (0..20)
            .map(|i| if i < 5 { make_trade(1.0, 0.01) } else { make_trade(-5.0, -0.05) })
            .collect();
        assert!(sqn(&trades) < 0.0);
    }

    // ── monthly_returns ────────────────────────────────────────────────────────

    #[test]
    fn monthly_returns_two_months() {
        // Jan 2024: 10_000 → 11_000 (+10%), Feb 2024: 11_000 → 10_450 (-5%)
        let jan_end: i64 = 1706745600_000; // 2024-02-01 00:00 UTC (last point of Jan)
        let feb_end: i64 = 1709251200_000; // 2024-03-01 00:00 UTC (last point of Feb)
        let curve = vec![(0i64, 10_000.0), (jan_end, 11_000.0), (feb_end, 10_450.0)];
        let mr = monthly_returns(&curve);
        assert_eq!(mr.len(), 2);
        // first entry: return from initial → jan_end
        assert!((mr[0].2 - 10.0).abs() < 0.01);
        // second entry: return from jan_end → feb_end
        assert!((mr[1].2 - (-5.0)).abs() < 0.01);
    }

    #[test]
    fn monthly_returns_empty() {
        assert!(monthly_returns(&[]).is_empty());
        assert!(monthly_returns(&[(0, 10_000.0)]).is_empty());
    }

    #[test]
    fn ts_to_year_month_known() {
        // 2024-01-15 → year=2024, month=1
        let ts_ms = 1705276800_000i64; // 2024-01-15 00:00 UTC
        let (y, m) = ts_to_year_month(ts_ms);
        assert_eq!(y, 2024);
        assert_eq!(m, 1);
    }

    // ── normal_cdf / probit ────────────────────────────────────────────────────

    #[test]
    fn normal_cdf_known_values() {
        assert!((normal_cdf(0.0)  - 0.5).abs()    < 1e-6);
        assert!((normal_cdf(1.645) - 0.95).abs()  < 1e-4);
        assert!((normal_cdf(-1.96) - 0.025).abs() < 1e-4);
        assert!(normal_cdf(10.0)  > 0.9999);
        assert!(normal_cdf(-10.0) < 0.0001);
    }

    #[test]
    fn probit_roundtrip() {
        for p in [0.01, 0.05, 0.25, 0.5, 0.75, 0.95, 0.99] {
            let x = probit(p);
            let p2 = normal_cdf(x);
            assert!((p2 - p).abs() < 1e-7, "roundtrip failed at p={p}: got {p2}");
        }
    }

    // ── skewness / excess_kurtosis ─────────────────────────────────────────────

    #[test]
    fn skewness_symmetric() {
        // Symmetric distribution → skewness ≈ 0
        let v: Vec<f64> = (-50..=50).map(|i| i as f64).collect();
        assert!(skewness(&v).abs() < 1e-10);
    }

    #[test]
    fn skewness_right_skewed() {
        // Right tail → positive skewness
        let mut v = vec![1.0_f64; 90];
        v.extend(vec![100.0; 10]);
        assert!(skewness(&v) > 0.0);
    }

    #[test]
    fn excess_kurtosis_normal_approx() {
        // Large iid normal sample → excess kurtosis ≈ 0
        // Use a fixed deterministic series that approximates normal
        let v: Vec<f64> = (1..=200)
            .map(|i| { let x = i as f64 / 201.0; probit(x) })
            .collect();
        assert!(excess_kurtosis(&v).abs() < 0.5);
    }

    // ── psr ────────────────────────────────────────────────────────────────────

    #[test]
    fn psr_clearly_positive() {
        // High mean, low variance → PSR near 1
        // Returns: 80% at +0.2%, 20% at +0.05%  (always positive, varying)
        let returns: Vec<f64> = (0..500)
            .map(|i| if i % 5 == 0 { 0.0005 } else { 0.002 })
            .collect();
        assert!(psr(&returns, 0.0) > 0.99);
    }

    #[test]
    fn psr_clearly_negative() {
        // Low mean, low variance → PSR near 0
        let returns: Vec<f64> = (0..500)
            .map(|i| if i % 5 == 0 { -0.0005 } else { -0.002 })
            .collect();
        assert!(psr(&returns, 0.0) < 0.01);
    }

    #[test]
    fn psr_near_zero_returns() {
        // Mean ≈ 0 → PSR ≈ 0.5
        let returns: Vec<f64> = (0..200)
            .map(|i| if i % 2 == 0 { 0.001 } else { -0.001 })
            .collect();
        let p = psr(&returns, 0.0);
        assert!((p - 0.5).abs() < 0.1);
    }

    // ── dsr ────────────────────────────────────────────────────────────────────

    #[test]
    fn dsr_single_trial_equals_psr() {
        let returns: Vec<f64> = (0..200).map(|i| if i % 3 == 0 { 0.002 } else { -0.001 }).collect();
        assert!((dsr(&returns, 1) - psr(&returns, 0.0)).abs() < 1e-10);
    }

    #[test]
    fn dsr_decreases_with_more_trials() {
        // Same returns, more trials → lower DSR (higher penalty for search)
        let returns: Vec<f64> = (0..300).map(|i| if i % 3 == 0 { 0.003 } else { -0.001 }).collect();
        let dsr1   = dsr(&returns, 10);
        let dsr100 = dsr(&returns, 1000);
        assert!(dsr1 > dsr100, "DSR should decrease as n_trials grows: {dsr1} vs {dsr100}");
    }
}
