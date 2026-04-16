use alm_core::trade::Trade;

pub fn daily_returns(equity: &[f64]) -> Vec<f64> {
    equity.windows(2).map(|w| (w[1] - w[0]) / w[0]).collect()
}

pub fn mean(v: &[f64]) -> f64 {
    if v.is_empty() {
        return 0.0;
    }
    v.iter().sum::<f64>() / v.len() as f64
}

pub fn std_dev(v: &[f64]) -> f64 {
    if v.len() < 2 {
        return 0.0;
    }
    let m = mean(v);
    let var = v.iter().map(|x| (x - m).powi(2)).sum::<f64>() / (v.len() - 1) as f64;
    var.sqrt()
}

pub fn sharpe_ratio(daily_returns: &[f64], risk_free_daily: f64) -> f64 {
    if daily_returns.len() < 2 {
        return 0.0;
    }
    let excess: Vec<f64> = daily_returns.iter().map(|r| r - risk_free_daily).collect();
    let m = mean(&excess);
    let s = std_dev(&excess);
    if s < f64::EPSILON {
        return 0.0;
    }
    m / s * (252_f64).sqrt()
}

pub fn sortino_ratio(daily_returns: &[f64], risk_free_daily: f64) -> f64 {
    if daily_returns.len() < 2 {
        return 0.0;
    }
    let excess: Vec<f64> = daily_returns.iter().map(|r| r - risk_free_daily).collect();
    let m = mean(&excess);
    let downside: Vec<f64> = excess
        .iter()
        .filter(|&&r| r < 0.0)
        .map(|&r| r.powi(2))
        .collect();
    if downside.is_empty() {
        return f64::INFINITY;
    }
    let downside_dev = (downside.iter().sum::<f64>() / downside.len() as f64).sqrt();
    if downside_dev < f64::EPSILON {
        return 0.0;
    }
    m / downside_dev * (252_f64).sqrt()
}

/// Compute Sharpe and Sortino ratios in a single pass over `daily_returns`,
/// avoiding two separate `excess: Vec<f64>` allocations.
pub fn sharpe_sortino(daily_returns: &[f64], risk_free_daily: f64) -> (f64, f64) {
    let n = daily_returns.len();
    if n < 2 {
        return (0.0, 0.0);
    }

    // Pass 1: mean of excess returns.
    let excess_mean =
        daily_returns.iter().map(|r| r - risk_free_daily).sum::<f64>() / n as f64;

    // Pass 2: variance of excess + downside deviation simultaneously.
    let mut sum_sq_dev = 0.0_f64;
    let mut downside_sq_sum = 0.0_f64;
    let mut downside_count = 0usize;
    for &r in daily_returns {
        let e = r - risk_free_daily;
        let dev = e - excess_mean;
        sum_sq_dev += dev * dev;
        if e < 0.0 {
            downside_sq_sum += e * e;
            downside_count += 1;
        }
    }

    let sqrt252 = (252_f64).sqrt();
    let std = (sum_sq_dev / (n - 1) as f64).sqrt();
    let sharpe = if std < f64::EPSILON { 0.0 } else { excess_mean / std * sqrt252 };

    let sortino = if downside_count == 0 {
        f64::INFINITY
    } else {
        let downside_dev = (downside_sq_sum / downside_count as f64).sqrt();
        if downside_dev < f64::EPSILON { 0.0 } else { excess_mean / downside_dev * sqrt252 }
    };

    (sharpe, sortino)
}

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

// ── Rolling metrics ───────────────────────────────────────────────────────────

/// Rolling Sharpe ratio over a sliding window of equity-curve returns.
/// Returns a Vec with the same length as `equity` (zero-padded for the warm-up).
pub fn rolling_sharpe(equity: &[f64], window: usize) -> Vec<f64> {
    if equity.len() < 2 {
        return vec![0.0; equity.len()];
    }
    let returns = daily_returns(equity);
    let mut result = vec![0.0_f64; equity.len()];
    let sqrt252 = (252_f64).sqrt();
    for i in 0..returns.len() {
        if i + 1 < window {
            // warm-up: not enough bars yet
            result[i + 1] = 0.0;
            continue;
        }
        let slice = &returns[(i + 1 - window)..=i];
        let m = mean(slice);
        let s = std_dev(slice);
        result[i + 1] = if s < f64::EPSILON { 0.0 } else { m / s * sqrt252 };
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

/// Value at Risk (95%) and Conditional VaR (CVaR/ES) from daily returns.
/// Returns `(var_95, cvar_95)` as positive fractions.
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

/// Omega ratio: sum of gains / sum of losses above/below `threshold`.
/// `threshold = 0.0` by default (absolute returns).
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

/// Tail ratio: abs(95th percentile return) / abs(5th percentile return).
/// Measures the ratio of right-tail gains to left-tail losses.
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

/// Recovery factor: total_return / max_drawdown (both as raw fractions, not pct).
pub fn recovery_factor(total_return: f64, max_drawdown: f64) -> f64 {
    if max_drawdown.abs() < f64::EPSILON {
        0.0
    } else {
        total_return / max_drawdown.abs()
    }
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
        assert_eq!(sharpe_ratio(&[], 0.0), 0.0);
    }

    #[test]
    fn sharpe_flat_returns() {
        // All returns equal rf → excess always 0 → sharpe = 0
        let r = vec![0.001; 252];
        assert_eq!(sharpe_ratio(&r, 0.001), 0.0);
    }

    #[test]
    fn sharpe_positive() {
        // Alternating +0.01 / +0.005 → mean > 0, std > 0 → Sharpe > 0
        let r: Vec<f64> = (0..252).map(|i| if i % 2 == 0 { 0.01 } else { 0.005 }).collect();
        assert!(sharpe_ratio(&r, 0.0) > 0.0);
    }

    #[test]
    fn sharpe_negative() {
        // Alternating -0.01 / -0.005 → mean < 0 → Sharpe < 0
        let r: Vec<f64> = (0..252).map(|i| if i % 2 == 0 { -0.01 } else { -0.005 }).collect();
        assert!(sharpe_ratio(&r, 0.0) < 0.0);
    }

    // ── sortino_ratio ──────────────────────────────────────────────────────────

    #[test]
    fn sortino_no_downside() {
        // Only positive returns → sortino = +inf
        let r = vec![0.005; 100];
        assert_eq!(sortino_ratio(&r, 0.0), f64::INFINITY);
    }

    #[test]
    fn sortino_positive() {
        let mut r = vec![0.003; 100];
        r.extend(vec![-0.001; 20]);
        assert!(sortino_ratio(&r, 0.0) > 0.0);
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
}
