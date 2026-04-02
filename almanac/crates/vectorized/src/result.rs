use serde::{Deserialize, Serialize};

/// Result of a vectorized backtest — lightweight metrics.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct VectorizedResult {
    pub strategy: String,
    pub symbol: String,

    // Capital
    pub initial_capital: f64,
    pub final_equity: f64,

    // Returns
    pub total_return_pct: f64,
    pub cagr_pct: f64,

    // Risk
    pub sharpe_ratio: f64,
    pub sortino_ratio: f64,
    pub max_drawdown_pct: f64,

    // Trade stats
    pub total_trades: usize,
    pub win_rate_pct: f64,
    pub profit_factor: f64,

    // Equity curve
    pub equity_curve: Vec<f64>,
}

impl VectorizedResult {
    pub fn compute(
        strategy: &str,
        symbol: &str,
        initial_capital: f64,
        equity_curve: Vec<f64>,
        pnl_per_trade: &[f64],
    ) -> Self {
        let final_eq = equity_curve.last().copied().unwrap_or(initial_capital);
        let total_return = (final_eq - initial_capital) / initial_capital * 100.0;

        // Daily returns from equity curve
        let daily_returns: Vec<f64> = equity_curve
            .windows(2)
            .map(|w| (w[1] - w[0]) / w[0])
            .collect();

        // Annualized metrics
        let n = daily_returns.len() as f64;
        let mean_ret = if n > 0.0 {
            daily_returns.iter().sum::<f64>() / n
        } else {
            0.0
        };

        let std_dev = if n > 1.0 {
            let var = daily_returns
                .iter()
                .map(|r| (r - mean_ret).powi(2))
                .sum::<f64>()
                / (n - 1.0);
            var.sqrt()
        } else {
            0.0
        };

        let sharpe = if std_dev > f64::EPSILON {
            mean_ret / std_dev * 252_f64.sqrt()
        } else {
            0.0
        };

        // Sortino
        let downside: Vec<f64> = daily_returns
            .iter()
            .filter(|&&r| r < 0.0)
            .map(|r| r.powi(2))
            .collect();
        let downside_dev = if !downside.is_empty() {
            (downside.iter().sum::<f64>() / downside.len() as f64).sqrt()
        } else {
            0.0
        };
        let sortino = if downside_dev > f64::EPSILON {
            mean_ret / downside_dev * 252_f64.sqrt()
        } else {
            0.0
        };

        // Max drawdown
        let mut peak = initial_capital;
        let mut max_dd = 0.0f64;
        for &eq in &equity_curve {
            if eq > peak {
                peak = eq;
            }
            let dd = (peak - eq) / peak;
            if dd > max_dd {
                max_dd = dd;
            }
        }

        // CAGR
        let n_bars = equity_curve.len();
        let duration_years = n_bars as f64 / 252.0; // approximate trading days
        let cagr = if duration_years > 0.0 {
            ((final_eq / initial_capital).powf(1.0 / duration_years) - 1.0) * 100.0
        } else {
            0.0
        };

        // Trade stats
        let total_trades = pnl_per_trade.len();
        let wins: Vec<&f64> = pnl_per_trade.iter().filter(|&&p| p > 0.0).collect();
        let losses: Vec<&f64> = pnl_per_trade.iter().filter(|&&p| p <= 0.0).collect();
        let win_rate = if total_trades > 0 {
            wins.len() as f64 / total_trades as f64 * 100.0
        } else {
            0.0
        };
        let gross_profit: f64 = wins.iter().map(|&&p| p).sum();
        let gross_loss: f64 = losses.iter().map(|&&p| p.abs()).sum();
        let profit_factor = if gross_loss > f64::EPSILON {
            gross_profit / gross_loss
        } else if gross_profit > 0.0 {
            f64::INFINITY
        } else {
            0.0
        };

        Self {
            strategy: strategy.to_string(),
            symbol: symbol.to_string(),
            initial_capital,
            final_equity: final_eq,
            total_return_pct: total_return,
            cagr_pct: cagr,
            sharpe_ratio: sharpe,
            sortino_ratio: sortino,
            max_drawdown_pct: max_dd * 100.0,
            total_trades,
            win_rate_pct: win_rate,
            profit_factor,
            equity_curve,
        }
    }
}
