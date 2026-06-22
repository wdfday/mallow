use crate::metrics::{self, ulcer_index, serenity_ratio, kelly_pct, trades_per_year, yearly_returns, DirectionStats,
    payoff_ratio, breakeven_win_rate, gross_profit_loss_usd, avg_bars_held_by_outcome, mfe_capture_ratio};
use alm_core::{exit::ExitReason, portfolio::Portfolio, regime::RegimeSummary, Timeframe};
use serde::{Deserialize, Serialize};

/// Breakdown of trade closures by exit type.
///
/// Each field counts the number of trades that were exited via that particular
/// [`alm_core::exit::ExitReason`] variant. Populated by [`BacktestReport::generate`].
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ExitReasonBreakdown {
    pub signal: usize,
    pub stop_loss: usize,
    pub take_profit: usize,
    pub trailing_stop: usize,
    pub max_bars: usize,
    pub end_of_data: usize,
}

/// Complete performance report produced after a backtest run.
///
/// Aggregates every relevant metric for evaluating a trading strategy:
/// return, risk-adjusted ratios, drawdown, per-trade statistics, distribution
/// shape, rolling series, and regime context.
///
/// Produced by [`BacktestReport::generate`] from a completed [`alm_core::portfolio::Portfolio`].
/// The `regime_summary` field is filled in separately by the engine after `run()` completes.
///
/// All `_pct` scalar fields are in **percentage points** (e.g. `20.0` = 20%), except
/// `win_rate_pct` which is also in percentage points. `psr` is a probability `[0, 1]`.
/// All `_usd` fields are in the same currency unit as `initial_capital`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BacktestReport {
    pub strategy: String,
    pub symbol: String,

    // ── Capital ───────────────────────────────────────────────────────────────
    /// Starting equity passed to the backtest engine.
    pub initial_capital: f64,
    /// Equity at the last bar of the backtest.
    pub final_equity: f64,

    // ── Return metrics ────────────────────────────────────────────────────────
    /// Simple total return `(final - initial) / initial × 100`. Not time-normalised.
    pub total_return_pct: f64,
    /// Compound Annual Growth Rate — geometric mean annual return. Use for period comparison.
    pub cagr_pct: f64,
    /// Annualised standard deviation of daily returns × 100. Represents ±1σ band per year.
    pub annualized_volatility_pct: f64,

    // ── Risk-adjusted ratios ──────────────────────────────────────────────────
    /// Excess return per unit of total volatility. >1 acceptable, >2 good, >3 excellent.
    pub sharpe_ratio: f64,
    /// Like Sharpe but penalises only downside deviation. Better for asymmetric profiles.
    pub sortino_ratio: f64,
    /// CAGR% / max_drawdown%. >0.5 adequate, >1 good, >3 excellent.
    pub calmar_ratio: f64,

    // ── Drawdown metrics ──────────────────────────────────────────────────────
    /// Largest peak-to-trough equity decline over the backtest, in percentage points.
    pub max_drawdown_pct: f64,
    /// Number of bars from the peak to the trough of the worst drawdown.
    pub max_dd_duration_bars: usize,
    /// Mean drawdown across all bars where equity is below its running peak.
    pub avg_drawdown_pct: f64,
    /// RMS of all drawdown depths (%). Captures both depth and duration. Lower is better.
    pub ulcer_index: f64,
    /// CAGR% / Ulcer Index. Risk-adjusted return accounting for drawdown duration.
    pub serenity_ratio: f64,

    // ── Trade statistics ──────────────────────────────────────────────────────
    /// Total number of completed (closed) round-trip trades.
    pub total_trades: usize,
    /// Fraction of trades that closed at a profit, expressed as percentage points.
    pub win_rate_pct: f64,
    /// Gross profit / gross loss (currency). >1.2 acceptable, >1.5 good, >2 excellent.
    pub profit_factor: f64,
    /// Expected return per trade as fraction of position size: `win_rate × avg_win − loss_rate × avg_loss`.
    pub expectancy: f64,
    /// Mean `pnl_pct` of winning trades (percentage points).
    pub avg_win_pct: f64,
    /// Mean absolute `pnl_pct` of losing trades (positive percentage points).
    pub avg_loss_pct: f64,
    /// Mean time between entry and exit across all trades, in hours.
    pub avg_trade_duration_hours: f64,
    /// Longest unbroken streak of consecutive losing trades.
    pub max_consecutive_losses: usize,
    /// Longest unbroken streak of consecutive winning trades.
    pub max_consecutive_wins: usize,
    /// Single-trade maximum winning return (percentage points).
    pub largest_win_pct: f64,
    /// Single-trade maximum losing return, reported positive (percentage points).
    pub largest_loss_pct: f64,
    /// Average win size / average loss size. Together with win rate, determines expectancy.
    pub payoff_ratio: f64,
    /// Minimum win rate needed to break even at the current payoff ratio (percentage points).
    pub breakeven_win_rate_pct: f64,
    /// Sum of all winning trades' PnL in currency units.
    pub gross_profit_usd: f64,
    /// Sum of all losing trades' PnL (positive) in currency units.
    pub gross_loss_usd: f64,
    /// Average bars held open for winning trades.
    pub avg_bars_held_winners: f64,
    /// Average bars held open for losing trades. Longer than winners signals "letting losers run".
    pub avg_bars_held_losers: f64,
    /// Average number of trades per calendar year.
    pub trades_per_year: f64,
    /// Sum of all commissions paid in currency units.
    pub total_commission_paid: f64,

    // ── MAE / MFE ─────────────────────────────────────────────────────────────
    /// Average Maximum Adverse Excursion (worst intra-trade loss) as % of entry price.
    pub avg_mae_pct: f64,
    /// Average Maximum Favourable Excursion (best intra-trade profit) as % of entry price.
    pub avg_mfe_pct: f64,
    /// For winning trades: mean(pnl / mfe) — fraction of peak move actually captured [0, 1].
    pub mfe_capture_ratio: f64,
    /// avg_mae / avg_mfe. >1 means the strategy spends more time losing intra-trade than gaining.
    pub mae_mfe_ratio: f64,

    // ── Advanced risk metrics ─────────────────────────────────────────────────
    /// Empirical 5th-percentile daily loss (positive fraction). Exceeded on 5% of days.
    pub var_95: f64,
    /// Mean of the worst 5% of daily returns (positive fraction). Also called Expected Shortfall.
    pub cvar_95: f64,
    /// Probability-weighted gains / losses at 0% threshold. >1 means net-positive distribution.
    pub omega_ratio: f64,
    /// |p95 daily return| / |p5 daily return|. >1 means right-tail gains exceed left-tail losses.
    pub tail_ratio: f64,
    /// Total return / max drawdown (both fractions). How much was earned per unit of peak loss.
    pub recovery_factor: f64,

    // ── Return distribution shape ─────────────────────────────────────────────
    /// Third standardised moment of daily returns. Positive = right tail (occasional large gains).
    pub skewness: f64,
    /// Fourth standardised moment − 3. Positive = fatter tails than normal.
    pub excess_kurtosis: f64,
    /// P(true Sharpe > 0) — statistical probability this strategy has a genuine edge [0, 1].
    pub psr: f64,

    // ── Position sizing guidance ──────────────────────────────────────────────
    /// Van Tharp's System Quality Number. <1.6 poor, 2–2.4 average, 3–5 excellent, >5 holy-grail.
    pub sqn: f64,
    /// Kelly Criterion: optimal fraction of capital to risk per trade. Use half-Kelly in practice.
    pub kelly_pct: f64,

    // ── Long / short breakdown ────────────────────────────────────────────────
    /// Trade statistics for long (Buy) trades only.
    pub long_stats: DirectionStats,
    /// Trade statistics for short (Sell) trades only.
    pub short_stats: DirectionStats,

    // ── Temporal breakdown ────────────────────────────────────────────────────
    /// Monthly returns as `(year, month 1–12, return_pct)`.
    pub monthly_returns: Vec<(i32, u32, f64)>,
    /// Annual returns as `(year, annual_return_pct)` derived by compounding monthly returns.
    pub yearly_returns: Vec<(i32, f64)>,

    // ── Rolling metrics (per-bar arrays) ──────────────────────────────────────
    /// 30-bar rolling Sharpe ratio at each equity-curve point. Zero-padded during warmup.
    pub rolling_sharpe: Vec<f64>,
    /// Drawdown percentage at each equity-curve point (distance from running peak).
    pub rolling_drawdown: Vec<f64>,
    /// Std dev of the rolling Sharpe series. High value = inconsistent performance across time.
    pub rolling_sharpe_std: f64,

    // ── Context ───────────────────────────────────────────────────────────────
    /// Bar timeframe detected from equity-curve timestamps.
    pub timeframe: Timeframe,
    /// Regime statistics populated by the engine after `run()` completes. `None` if no regime filter was active.
    pub regime_summary: Option<RegimeSummary>,
    /// Breakdown of trade exits by type.
    pub exit_reasons: ExitReasonBreakdown,
}

impl BacktestReport {
    /// Build a complete [`BacktestReport`] from a finished backtest portfolio.
    ///
    /// - `strategy_name` — label stored in `report.strategy` for display/serialisation.
    /// - `symbol` — label stored in `report.symbol`.
    /// - `portfolio` — completed portfolio with `equity_curve` and `trades` populated by the engine.
    /// - `risk_free_annual` — annualised risk-free rate as a fraction (e.g. `0.05` = 5%).
    ///   Used to compute excess returns for Sharpe and Sortino.
    ///
    /// Sharpe and Sortino are computed on **daily** returns (equity aggregated to one value per
    /// calendar day) regardless of bar frequency, so M1 and D1 backtests remain comparable.
    /// The `annualization_factor` is derived empirically from the equity curve timestamps:
    /// US stocks → ~252, crypto → ~365.
    ///
    /// When `portfolio.trades` is empty, Sharpe, Sortino, and PSR are forced to `0.0` to
    /// avoid noise from a flat equity curve against a non-zero risk-free rate.
    pub fn generate(
        strategy_name: &str,
        symbol: &str,
        portfolio: &Portfolio,
        risk_free_annual: f64,
    ) -> Self {
        let equity: Vec<f64> = portfolio.equity_curve.iter().map(|p| p.equity).collect();
        let n = equity.len();

        let initial = portfolio.initial_capital;
        let final_eq = equity.last().copied().unwrap_or(initial);
        let total_return = (final_eq - initial) / initial * 100.0;

        // Duration in years from equity curve timestamps
        let duration_years = if n > 1 {
            let start = portfolio.equity_curve.first().unwrap().timestamp;
            let end = portfolio.equity_curve.last().unwrap().timestamp;
            (end - start) as f64 / (365.25 * 24.0 * 3600.0 * 1000.0)
        } else {
            1.0
        };

        let cagr = if duration_years > 0.0 {
            ((final_eq / initial).powf(1.0 / duration_years) - 1.0) * 100.0
        } else {
            0.0
        };

        // Detect timeframe from equity curve timestamps.
        let timeframe = Timeframe::detect(
            &portfolio.equity_curve.iter().map(|p| p.timestamp).collect::<Vec<_>>(),
        );

        // --- Annualization via daily returns -----------------------------------
        // Aggregate the equity curve to one value per calendar day (last bar of day).
        // Sharpe/Sortino are always expressed in daily units regardless of bar freq:
        //   - M1 and H1 backtests become comparable
        //   - Consecutive identical bars (no open position) don't inflate variance
        // `days_per_year` is empirical (actual trading days / calendar years):
        //   US stocks → ~252, crypto → ~365, both correct automatically.
        let (daily_equity, days_per_year) = aggregate_to_daily(portfolio);
        let daily_returns = metrics::bar_returns(&daily_equity);

        let ann_vol = metrics::std_dev(&daily_returns) * days_per_year.sqrt() * 100.0;

        let risk_free_daily = (1.0 + risk_free_annual).powf(1.0 / days_per_year) - 1.0;
        let (sharpe_raw, sortino_raw) = metrics::sharpe_sortino(&daily_returns, risk_free_daily, days_per_year);

        let (max_dd, max_dd_bars, avg_dd) = metrics::drawdown_stats(&equity);
        let calmar = if max_dd.abs() > f64::EPSILON {
            cagr / (max_dd * 100.0)
        } else {
            0.0
        };

        let trades = &portfolio.trades;
        let total_trades = trades.len();

        // Guard: ratio metrics are meaningless (and noisy) with no trades.
        // e.g. flat equity + positive risk-free → negative mean excess → negative Sortino.
        let no_trades = total_trades == 0;
        let sharpe  = if no_trades { 0.0 } else { sharpe_raw };
        let sortino = if no_trades { 0.0 } else { sortino_raw };

        let (win_rate, pf, expectancy, avg_win, avg_loss) = metrics::trade_stats(trades);
        let avg_duration = if total_trades > 0 {
            trades.iter().map(|t| t.duration_hours()).sum::<f64>() / total_trades as f64
        } else {
            0.0
        };
        let skewness       = metrics::skewness(&daily_returns);
        let excess_kurtosis = metrics::excess_kurtosis(&daily_returns);
        let psr            = if no_trades { 0.0 } else { metrics::psr(&daily_returns, 0.0) };

        let max_consec_losses = metrics::max_consecutive_losses(trades);
        let max_consec_wins   = metrics::max_consecutive_wins(trades);
        let (largest_win, largest_loss) = metrics::largest_win_loss(trades);
        let sqn = metrics::sqn(trades);
        let (long_stats, short_stats) = metrics::direction_stats(trades);
        let monthly_returns = metrics::monthly_returns(
            &portfolio.equity_curve.iter().map(|p| (p.timestamp, p.equity)).collect::<Vec<_>>(),
        );

        // Advanced risk metrics — also daily-basis for consistency
        let (var_95, cvar_95) = metrics::var_cvar_95(&daily_returns);
        let omega = metrics::omega_ratio(&daily_returns, 0.0);
        let tail = metrics::tail_ratio(&daily_returns);
        let recovery = metrics::recovery_factor(total_return / 100.0, max_dd);

        // Rolling Sharpe uses bar-level granularity (visual sparkline).
        // ann_factor = days_per_year keeps the scale consistent with headline Sharpe.
        let rolling_sharpe = metrics::rolling_sharpe(&equity, 30, days_per_year);
        let rolling_drawdown = metrics::rolling_drawdown(&equity);

        let ui = ulcer_index(&equity);
        let serenity = serenity_ratio(cagr, ui);
        let kelly = kelly_pct(win_rate, avg_win * 100.0, avg_loss.abs() * 100.0);
        let tpy = trades_per_year(total_trades, duration_years);
        let total_commission: f64 = trades.iter().map(|t| t.commission).sum();
        let yearly = yearly_returns(&monthly_returns);
        let (avg_mae, avg_mfe) = if total_trades > 0 {
            let mae_sum: f64 = trades.iter().map(|t| t.mae_pct).sum();
            let mfe_sum: f64 = trades.iter().map(|t| t.mfe_pct).sum();
            (mae_sum / total_trades as f64 * 100.0, mfe_sum / total_trades as f64 * 100.0)
        } else {
            (0.0, 0.0)
        };

        let pr = payoff_ratio(avg_win, avg_loss);
        let bew = breakeven_win_rate(pr);
        let (gross_profit, gross_loss) = gross_profit_loss_usd(trades);
        let (avg_bars_winners, avg_bars_losers) = avg_bars_held_by_outcome(trades);
        let mfe_capture = mfe_capture_ratio(trades);
        let mae_mfe = if avg_mfe.abs() > f64::EPSILON { avg_mae / avg_mfe } else { 0.0 };
        let rolling_sharpe_std = metrics::std_dev(&metrics::rolling_sharpe(&equity, 30, days_per_year));

        let mut exit_reasons = ExitReasonBreakdown::default();
        for t in trades.iter() {
            match t.exit_reason {
                ExitReason::Signal       => exit_reasons.signal += 1,
                ExitReason::StopLoss     => exit_reasons.stop_loss += 1,
                ExitReason::TakeProfit   => exit_reasons.take_profit += 1,
                ExitReason::TrailingStop => exit_reasons.trailing_stop += 1,
                ExitReason::MaxBarsHeld  => exit_reasons.max_bars += 1,
                ExitReason::EndOfData    => exit_reasons.end_of_data += 1,
            }
        }

        Self {
            strategy: strategy_name.to_string(),
            symbol: symbol.to_string(),
            initial_capital: initial,
            final_equity: final_eq,
            total_return_pct: total_return,
            cagr_pct: cagr,
            annualized_volatility_pct: ann_vol,
            sharpe_ratio: sharpe,
            sortino_ratio: sortino,
            calmar_ratio: calmar,
            max_drawdown_pct: max_dd * 100.0,
            max_dd_duration_bars: max_dd_bars,
            avg_drawdown_pct: avg_dd * 100.0,
            total_trades,
            win_rate_pct: win_rate * 100.0,
            profit_factor: pf,
            // pnl_pct is now a fraction → scale to percentage points like the
            // sibling aggregate fields (avg_win_pct, etc.).
            expectancy: expectancy * 100.0,
            avg_win_pct: avg_win * 100.0,
            avg_loss_pct: avg_loss * 100.0,
            avg_trade_duration_hours: avg_duration,
            skewness,
            excess_kurtosis,
            psr,
            max_consecutive_losses: max_consec_losses,
            max_consecutive_wins: max_consec_wins,
            largest_win_pct: largest_win * 100.0,
            largest_loss_pct: largest_loss * 100.0,
            sqn,
            long_stats,
            short_stats,
            monthly_returns,
            var_95,
            cvar_95,
            omega_ratio: omega,
            tail_ratio: tail,
            recovery_factor: recovery,
            rolling_sharpe,
            rolling_drawdown,
            timeframe,
            regime_summary: None, // populated by Engine after run()
            ulcer_index: ui,
            serenity_ratio: serenity,
            kelly_pct: kelly,
            trades_per_year: tpy,
            total_commission_paid: total_commission,
            yearly_returns: yearly,
            avg_mae_pct: avg_mae,
            avg_mfe_pct: avg_mfe,
            payoff_ratio: pr,
            breakeven_win_rate_pct: bew,
            gross_profit_usd: gross_profit,
            gross_loss_usd: gross_loss,
            avg_bars_held_winners: avg_bars_winners,
            avg_bars_held_losers: avg_bars_losers,
            mfe_capture_ratio: mfe_capture,
            mae_mfe_ratio: mae_mfe,
            rolling_sharpe_std,
            exit_reasons,
        }
    }
}

// ── Metric metadata ───────────────────────────────────────────────────────────

/// Metadata for a single backtest metric field — used to render contextual help in the UI.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MetricMeta {
    /// Field name as it appears in `BacktestReport` JSON (snake_case).
    pub field: &'static str,
    /// Short human-readable label for display (e.g. "Sharpe Ratio").
    pub label: &'static str,
    /// Unit of the value (e.g. "%", "ratio", "currency", "bars", "probability").
    pub unit: &'static str,
    /// Plain-language description in English for a non-technical audience.
    pub description: &'static str,
    /// Threshold guide for interpretation in English, if applicable (empty otherwise).
    pub thresholds: &'static str,
    /// Mô tả bằng tiếng Việt dành cho người dùng phổ thông.
    pub description_vi: &'static str,
    /// Hướng dẫn ngưỡng đánh giá bằng tiếng Việt (rỗng nếu không có).
    pub thresholds_vi: &'static str,
}

impl BacktestReport {
    /// Return a static catalog of all scalar metric fields in `BacktestReport`.
    ///
    /// Each entry carries the field name, display label, unit, a plain-language description,
    /// and an optional threshold guide. Intended for API responses that power UI tooltips
    /// and report legends — the frontend can look up any field by name.
    pub fn catalog() -> &'static [MetricMeta] {
        &[
            // ── Return ────────────────────────────────────────────────────────
            MetricMeta {
                field: "total_return_pct",
                label: "Total Return",
                unit: "%",
                description: "How much the portfolio gained or lost in total over the entire backtest period. Not adjusted for time — a 50% gain over 10 years is treated the same as 50% over 10 days.",
                thresholds: "",
                description_vi: "Tổng lợi nhuận hoặc thua lỗ của danh mục trong toàn bộ giai đoạn backtest. Không điều chỉnh theo thời gian — lãi 50% sau 10 năm và 10 ngày đều hiển thị như nhau.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "cagr_pct",
                label: "CAGR",
                unit: "%/yr",
                description: "Compound Annual Growth Rate — the geometric mean return per year. Accounts for the length of the backtest, so strategies of different durations can be compared fairly.",
                thresholds: "",
                description_vi: "Tốc độ tăng trưởng kép hàng năm — lợi nhuận trung bình hình học mỗi năm. Tính đến độ dài backtest nên có thể so sánh chiến lược ở các khoảng thời gian khác nhau một cách công bằng.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "annualized_volatility_pct",
                label: "Volatility",
                unit: "%/yr",
                description: "How much the daily returns fluctuate, scaled to a yearly figure. Higher volatility means a bumpier ride — the account swings more from day to day.",
                thresholds: "",
                description_vi: "Mức độ biến động của lợi nhuận hàng ngày, được quy đổi sang đơn vị năm. Biến động cao hơn đồng nghĩa với tài khoản dao động mạnh hơn theo từng ngày.",
                thresholds_vi: "",
            },
            // ── Risk-adjusted ─────────────────────────────────────────────────
            MetricMeta {
                field: "sharpe_ratio",
                label: "Sharpe Ratio",
                unit: "ratio",
                description: "How much return was earned per unit of risk (volatility). A higher value means the strategy delivered more profit relative to the ups and downs it experienced.",
                thresholds: ">1 acceptable · >2 good · >3 excellent",
                description_vi: "Lợi nhuận thu được trên mỗi đơn vị rủi ro (biến động). Giá trị cao hơn nghĩa là chiến lược tạo ra nhiều lợi nhuận hơn so với mức độ dao động mà nó trải qua.",
                thresholds_vi: ">1 chấp nhận được · >2 tốt · >3 xuất sắc",
            },
            MetricMeta {
                field: "sortino_ratio",
                label: "Sortino Ratio",
                unit: "ratio",
                description: "Like the Sharpe Ratio, but only counts downside volatility (bad days). A strategy that has large gains but small losses will score higher here than on Sharpe.",
                thresholds: ">1 acceptable · >2 good",
                description_vi: "Tương tự Sharpe Ratio nhưng chỉ tính biến động giảm (ngày thua lỗ). Chiến lược có lãi lớn nhưng lỗ nhỏ sẽ được đánh giá cao hơn ở chỉ số này.",
                thresholds_vi: ">1 chấp nhận được · >2 tốt",
            },
            MetricMeta {
                field: "calmar_ratio",
                label: "Calmar Ratio",
                unit: "ratio",
                description: "Annual return divided by the worst drawdown ever seen. Measures how much the strategy earned relative to its worst loss — a way to judge if the risk was worth it.",
                thresholds: ">0.5 adequate · >1 good · >3 excellent",
                description_vi: "Lợi nhuận hàng năm chia cho mức sụt giảm tệ nhất từng xảy ra. Đánh giá chiến lược kiếm được bao nhiêu so với khoản lỗ nặng nhất — thước đo rủi ro có xứng đáng hay không.",
                thresholds_vi: ">0.5 đủ dùng · >1 tốt · >3 xuất sắc",
            },
            MetricMeta {
                field: "serenity_ratio",
                label: "Serenity Ratio",
                unit: "ratio",
                description: "Annual return divided by the Ulcer Index. Rewards strategies that have both shallow drawdowns and short recovery periods — more comprehensive than Calmar alone.",
                thresholds: "",
                description_vi: "Lợi nhuận hàng năm chia cho Ulcer Index. Ưu tiên các chiến lược có drawdown nông lẫn thời gian phục hồi ngắn — toàn diện hơn Calmar Ratio.",
                thresholds_vi: "",
            },
            // ── Drawdown ──────────────────────────────────────────────────────
            MetricMeta {
                field: "max_drawdown_pct",
                label: "Max Drawdown",
                unit: "%",
                description: "The largest peak-to-trough decline in the account balance during the backtest. This is the worst loss a live trader would have experienced on paper.",
                thresholds: "",
                description_vi: "Mức sụt giảm từ đỉnh xuống đáy lớn nhất trong suốt quá trình backtest. Đây là khoản lỗ tệ nhất mà một trader thực tế sẽ phải chịu đựng.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "max_dd_duration_bars",
                label: "Max DD Duration",
                unit: "bars",
                description: "How many bars it took to go from the equity peak down to the bottom of the worst drawdown. Does not include recovery time.",
                thresholds: "",
                description_vi: "Số nến từ đỉnh vốn xuống đáy của đợt sụt giảm tệ nhất. Không tính thời gian phục hồi sau đó.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "avg_drawdown_pct",
                label: "Avg Drawdown",
                unit: "%",
                description: "The average amount the account was below its all-time high, measured only during periods when a drawdown was active. Captures the typical discomfort level of holding the strategy.",
                thresholds: "",
                description_vi: "Mức sụt giảm trung bình so với đỉnh cao nhất, chỉ tính trong các giai đoạn đang có drawdown. Phản ánh mức độ khó chịu thông thường khi giữ chiến lược.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "ulcer_index",
                label: "Ulcer Index",
                unit: "value",
                description: "A stress measure that accounts for both how deep and how long drawdowns last. A shallow drawdown that lasts months can score worse than a deep drawdown that recovers quickly. Lower is better.",
                thresholds: "",
                description_vi: "Chỉ số đo mức độ stress, tính cả độ sâu lẫn thời gian kéo dài của drawdown. Drawdown nông nhưng kéo dài nhiều tháng có thể tệ hơn drawdown sâu nhưng phục hồi nhanh. Càng thấp càng tốt.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "recovery_factor",
                label: "Recovery Factor",
                unit: "ratio",
                description: "Total return divided by max drawdown. A value above 1 means the strategy earned back more than its worst-ever loss.",
                thresholds: ">1 earned more than the worst loss",
                description_vi: "Tổng lợi nhuận chia cho max drawdown. Giá trị trên 1 nghĩa là chiến lược kiếm được nhiều hơn mức lỗ nặng nhất từng xảy ra.",
                thresholds_vi: ">1 kiếm được nhiều hơn mức lỗ tệ nhất",
            },
            // ── Trade stats ───────────────────────────────────────────────────
            MetricMeta {
                field: "total_trades",
                label: "Total Trades",
                unit: "count",
                description: "The number of completed (entry + exit) trades during the backtest. More trades generally produce more reliable statistics.",
                thresholds: "<30/yr = low confidence · >100 = statistically meaningful",
                description_vi: "Số lệnh đã hoàn thành (có cả vào và ra) trong backtest. Càng nhiều lệnh thì số liệu thống kê càng đáng tin cậy hơn.",
                thresholds_vi: "<30/năm = độ tin cậy thấp · >100 = có ý nghĩa thống kê",
            },
            MetricMeta {
                field: "win_rate_pct",
                label: "Win Rate",
                unit: "%",
                description: "The percentage of trades that ended at a profit. Meaningless on its own — a 30% win rate is perfectly fine if winners are 3× larger than losers.",
                thresholds: "",
                description_vi: "Tỷ lệ phần trăm lệnh có lợi nhuận. Không có nhiều ý nghĩa khi xét riêng lẻ — win rate 30% vẫn ổn nếu lệnh thắng lớn gấp 3 lần lệnh thua.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "profit_factor",
                label: "Profit Factor",
                unit: "ratio",
                description: "Total profit from winning trades divided by total loss from losing trades. Above 1 means the strategy made more than it lost overall.",
                thresholds: ">1.2 acceptable · >1.5 good · >2 excellent",
                description_vi: "Tổng lãi từ các lệnh thắng chia cho tổng lỗ từ các lệnh thua. Trên 1 nghĩa là chiến lược kiếm được nhiều hơn mất đi.",
                thresholds_vi: ">1.2 chấp nhận được · >1.5 tốt · >2 xuất sắc",
            },
            MetricMeta {
                field: "expectancy",
                label: "Expectancy",
                unit: "%",
                description: "The average expected return per trade as a percentage of the position size. Positive expectancy is the minimum requirement for a viable strategy.",
                thresholds: ">0 required",
                description_vi: "Lợi nhuận kỳ vọng trung bình trên mỗi lệnh, tính theo tỷ lệ phần trăm kích thước vị thế. Expectancy dương là điều kiện tối thiểu để một chiến lược có giá trị.",
                thresholds_vi: ">0 bắt buộc",
            },
            MetricMeta {
                field: "payoff_ratio",
                label: "Payoff Ratio",
                unit: "ratio",
                description: "Average winning trade size divided by average losing trade size. A ratio of 2 means winners are twice as large as losers on average.",
                thresholds: "",
                description_vi: "Kích thước lệnh thắng trung bình chia cho kích thước lệnh thua trung bình. Tỷ lệ 2 nghĩa là lệnh thắng trung bình lớn gấp đôi lệnh thua.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "breakeven_win_rate_pct",
                label: "Breakeven Win Rate",
                unit: "%",
                description: "The minimum win rate needed to break even given the current payoff ratio. If the actual win rate is above this, the strategy has a positive edge.",
                thresholds: "",
                description_vi: "Win rate tối thiểu cần có để không lỗ với tỷ lệ thắng/thua hiện tại. Nếu win rate thực tế cao hơn con số này thì chiến lược có lợi thế dương.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "avg_win_pct",
                label: "Avg Win",
                unit: "%",
                description: "The average return of profitable trades, expressed as a percentage of the position size.",
                thresholds: "",
                description_vi: "Lợi nhuận trung bình của các lệnh thắng, tính theo phần trăm kích thước vị thế.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "avg_loss_pct",
                label: "Avg Loss",
                unit: "%",
                description: "The average loss of unprofitable trades, expressed as a positive percentage of the position size.",
                thresholds: "",
                description_vi: "Mức lỗ trung bình của các lệnh thua, thể hiện dưới dạng số dương theo phần trăm kích thước vị thế.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "largest_win_pct",
                label: "Largest Win",
                unit: "%",
                description: "The single best trade return as a percentage. A very large value relative to the average may mean one outlier trade is distorting the results.",
                thresholds: "",
                description_vi: "Lệnh thắng đơn lẻ tốt nhất tính theo phần trăm. Giá trị quá lớn so với trung bình có thể là dấu hiệu một lệnh ngoại lệ đang làm lệch kết quả.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "largest_loss_pct",
                label: "Largest Loss",
                unit: "%",
                description: "The single worst trade loss as a positive percentage. Useful for checking whether one bad trade dominated the P&L.",
                thresholds: "",
                description_vi: "Lệnh thua đơn lẻ tệ nhất, thể hiện dưới dạng số dương. Hữu ích để kiểm tra xem có một lệnh xấu nào đang chi phối toàn bộ kết quả không.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "max_consecutive_losses",
                label: "Max Consecutive Losses",
                unit: "count",
                description: "The longest unbroken run of losing trades. Position sizing should ensure the account can survive this many max-loss trades in a row.",
                thresholds: "",
                description_vi: "Chuỗi thua liên tiếp dài nhất. Quản lý vốn cần đảm bảo tài khoản vẫn tồn tại được sau bấy nhiêu lệnh thua liên tục với kích thước lỗ tối đa.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "max_consecutive_wins",
                label: "Max Consecutive Wins",
                unit: "count",
                description: "The longest unbroken run of winning trades. High values can indicate momentum periods that may not repeat.",
                thresholds: "",
                description_vi: "Chuỗi thắng liên tiếp dài nhất. Giá trị cao có thể chỉ ra các giai đoạn xu hướng mạnh mà chưa chắc sẽ lặp lại.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "avg_trade_duration_hours",
                label: "Avg Trade Duration",
                unit: "hours",
                description: "The average time between a trade's entry and exit. Short durations can mean high turnover and sensitivity to execution costs.",
                thresholds: "",
                description_vi: "Thời gian trung bình từ lúc vào lệnh đến lúc thoát lệnh. Thời gian ngắn có thể đồng nghĩa với vòng quay cao và nhạy cảm hơn với phí giao dịch.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "trades_per_year",
                label: "Trades / Year",
                unit: "count/yr",
                description: "How many trades the strategy takes per calendar year on average. Affects both statistical reliability and transaction cost impact.",
                thresholds: "",
                description_vi: "Số lệnh trung bình mỗi năm dương lịch. Ảnh hưởng đến cả độ tin cậy thống kê lẫn tác động của phí giao dịch.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "gross_profit_usd",
                label: "Gross Profit",
                unit: "currency",
                description: "Total profit from all winning trades in currency units. Divide by gross loss to get the profit factor.",
                thresholds: "",
                description_vi: "Tổng lợi nhuận từ tất cả các lệnh thắng theo đơn vị tiền tệ. Chia cho gross loss để tính profit factor.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "gross_loss_usd",
                label: "Gross Loss",
                unit: "currency",
                description: "Total loss from all losing trades in currency units (positive value).",
                thresholds: "",
                description_vi: "Tổng thua lỗ từ tất cả các lệnh thua theo đơn vị tiền tệ (số dương).",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "total_commission_paid",
                label: "Total Commission",
                unit: "currency",
                description: "Sum of all transaction fees paid across the backtest. A high value relative to gross profit indicates the strategy is sensitive to costs.",
                thresholds: "",
                description_vi: "Tổng phí giao dịch đã trả trong suốt backtest. Giá trị cao so với gross profit cho thấy chiến lược nhạy cảm với chi phí.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "avg_bars_held_winners",
                label: "Avg Bars Held (Winners)",
                unit: "bars",
                description: "Average number of bars a winning trade was kept open.",
                thresholds: "",
                description_vi: "Số nến trung bình mà một lệnh thắng được giữ mở.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "avg_bars_held_losers",
                label: "Avg Bars Held (Losers)",
                unit: "bars",
                description: "Average number of bars a losing trade was kept open. If this is much larger than winning trade duration, the strategy may be 'letting losers run'.",
                thresholds: "",
                description_vi: "Số nến trung bình mà một lệnh thua được giữ mở. Nếu lớn hơn nhiều so với lệnh thắng, có thể chiến lược đang 'để lỗ chạy' — sai lầm phổ biến.",
                thresholds_vi: "",
            },
            // ── MAE / MFE ─────────────────────────────────────────────────────
            MetricMeta {
                field: "avg_mae_pct",
                label: "Avg MAE",
                unit: "%",
                description: "Maximum Adverse Excursion — the average worst intra-trade loss seen before the trade closed. Useful for calibrating stop-loss levels.",
                thresholds: "",
                description_vi: "Mức lỗ trong lệnh tệ nhất trung bình trước khi lệnh đóng. Dùng để căn chỉnh mức đặt stop-loss hợp lý.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "avg_mfe_pct",
                label: "Avg MFE",
                unit: "%",
                description: "Maximum Favourable Excursion — the average best intra-trade profit seen before the trade closed. The gap between this and the actual exit shows how much profit was left on the table.",
                thresholds: "",
                description_vi: "Mức lãi trong lệnh tốt nhất trung bình trước khi lệnh đóng. Khoảng cách giữa con số này và lãi thực tế cho thấy bao nhiêu lợi nhuận đã bị bỏ lại.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "mfe_capture_ratio",
                label: "MFE Capture",
                unit: "ratio [0–1]",
                description: "For winning trades: how much of the best intra-trade move was actually captured. A value of 0.7 means the strategy typically exited at 70% of the peak move.",
                thresholds: "Closer to 1 is better",
                description_vi: "Với lệnh thắng: tỷ lệ biến động tốt nhất trong lệnh thực sự được thu vào. Giá trị 0.7 nghĩa là chiến lược thường thoát ở mức 70% đỉnh di chuyển.",
                thresholds_vi: "Càng gần 1 càng tốt",
            },
            MetricMeta {
                field: "mae_mfe_ratio",
                label: "MAE/MFE Ratio",
                unit: "ratio",
                description: "Average adverse excursion divided by average favourable excursion. Above 1 means the strategy experiences more intra-trade loss than gain before closing.",
                thresholds: "<1 preferred",
                description_vi: "MAE trung bình chia cho MFE trung bình. Trên 1 nghĩa là trong lệnh thường mất nhiều hơn lãi trước khi đóng lệnh.",
                thresholds_vi: "<1 là tốt hơn",
            },
            // ── Advanced risk ─────────────────────────────────────────────────
            MetricMeta {
                field: "var_95",
                label: "VaR 95%",
                unit: "fraction",
                description: "Value at Risk at the 95% confidence level: the worst daily loss that is exceeded on only 5% of trading days. Expressed as a positive fraction.",
                thresholds: "",
                description_vi: "Giá trị rủi ro ở mức tin cậy 95%: mức lỗ ngày tệ nhất chỉ bị vượt qua 5% ngày giao dịch. Thể hiện dưới dạng phân số dương.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "cvar_95",
                label: "CVaR 95%",
                unit: "fraction",
                description: "Conditional Value at Risk (Expected Shortfall): the average of the worst 5% of daily losses. Unlike VaR, this captures how bad things get in the tail.",
                thresholds: "",
                description_vi: "VaR có điều kiện (Expected Shortfall): trung bình của 5% ngày lỗ tệ nhất. Khác VaR ở chỗ nó phản ánh mức độ tệ thực sự trong vùng đuôi phân phối.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "omega_ratio",
                label: "Omega Ratio",
                unit: "ratio",
                description: "The ratio of all daily gains above 0% to all daily losses below 0%. Uses the full return distribution, not just mean and variance. Above 1 means more probability-weighted gain than loss.",
                thresholds: ">1 profitable distribution",
                description_vi: "Tỷ lệ tổng lãi trên 0% so với tổng lỗ dưới 0% theo phân phối xác suất. Sử dụng toàn bộ phân phối lợi nhuận, không chỉ trung bình và phương sai. Trên 1 nghĩa là phân phối nghiêng về lãi.",
                thresholds_vi: ">1 phân phối có lợi",
            },
            MetricMeta {
                field: "tail_ratio",
                label: "Tail Ratio",
                unit: "ratio",
                description: "The 95th-percentile daily gain divided by the magnitude of the 5th-percentile daily loss. Above 1 means the best days are bigger than the worst days.",
                thresholds: ">1 asymmetric upside",
                description_vi: "Lãi ngày ở phân vị thứ 95 chia cho độ lớn lỗ ngày ở phân vị thứ 5. Trên 1 nghĩa là ngày tốt nhất lớn hơn ngày xấu nhất.",
                thresholds_vi: ">1 lợi thế bất đối xứng về phía tăng",
            },
            // ── Distribution shape ────────────────────────────────────────────
            MetricMeta {
                field: "skewness",
                label: "Skewness",
                unit: "value",
                description: "Asymmetry of the daily-return distribution. Positive skew means occasional large gains with frequent small losses (typical of trend-following). Negative skew means frequent small gains with occasional large losses.",
                thresholds: "",
                description_vi: "Độ lệch của phân phối lợi nhuận ngày. Lệch dương nghĩa là thỉnh thoảng lãi lớn nhưng thường xuyên lỗ nhỏ (điển hình của trend-following). Lệch âm là ngược lại.",
                thresholds_vi: "",
            },
            MetricMeta {
                field: "excess_kurtosis",
                label: "Excess Kurtosis",
                unit: "value",
                description: "How fat the tails of the return distribution are relative to a normal distribution. Most financial markets have positive excess kurtosis — extreme events happen more often than expected.",
                thresholds: "0 = normal · >0 = fatter tails",
                description_vi: "Độ dày của đuôi phân phối lợi nhuận so với phân phối chuẩn. Hầu hết thị trường tài chính có excess kurtosis dương — các sự kiện cực đoan xảy ra thường xuyên hơn lý thuyết.",
                thresholds_vi: "0 = phân phối chuẩn · >0 = đuôi dày hơn",
            },
            MetricMeta {
                field: "psr",
                label: "Probabilistic Sharpe",
                unit: "probability [0–1]",
                description: "The statistical probability that the strategy's true Sharpe Ratio is greater than zero, accounting for the non-normality of returns. Values close to 1 indicate high confidence in the edge.",
                thresholds: ">0.95 statistically confident",
                description_vi: "Xác suất thống kê rằng Sharpe Ratio thực sự của chiến lược lớn hơn 0, có tính đến sự không chuẩn của phân phối lợi nhuận. Giá trị gần 1 là có lợi thế thực sự.",
                thresholds_vi: ">0.95 có độ tin cậy thống kê",
            },
            // ── Position sizing ───────────────────────────────────────────────
            MetricMeta {
                field: "sqn",
                label: "SQN",
                unit: "score",
                description: "System Quality Number (Van Tharp): combines expectancy and consistency into a single score. Capped at 100 trades per the original definition.",
                thresholds: "<1.6 poor · 2–2.4 average · 2.5–2.9 good · >3 excellent · >5 exceptional",
                description_vi: "Chỉ số Chất lượng Hệ thống (Van Tharp): kết hợp kỳ vọng và tính nhất quán thành một điểm số duy nhất. Giới hạn tối đa 100 lệnh theo định nghĩa gốc.",
                thresholds_vi: "<1.6 kém · 2–2.4 trung bình · 2.5–2.9 tốt · >3 xuất sắc · >5 ngoại hạng",
            },
            MetricMeta {
                field: "kelly_pct",
                label: "Kelly %",
                unit: "fraction",
                description: "The theoretically optimal fraction of capital to risk per trade to maximise long-run growth. Negative means the edge is against you. In practice, use half this value to reduce variance.",
                thresholds: "Negative = no edge · Use half-Kelly in practice",
                description_vi: "Tỷ lệ vốn tối ưu theo lý thuyết nên đặt vào mỗi lệnh để tối đa hóa tăng trưởng dài hạn. Âm nghĩa là lợi thế đang nghiêng về phía thua. Thực tế nên dùng một nửa giá trị này.",
                thresholds_vi: "Âm = không có lợi thế · Thực tế nên dùng half-Kelly",
            },
            // ── Rolling ───────────────────────────────────────────────────────
            MetricMeta {
                field: "rolling_sharpe_std",
                label: "Sharpe Stability",
                unit: "ratio",
                description: "Standard deviation of the 30-bar rolling Sharpe Ratio. A high value means performance varies strongly across time periods — the strategy may be regime-dependent.",
                thresholds: "Lower = more consistent",
                description_vi: "Độ lệch chuẩn của Sharpe Ratio trượt 30 nến. Giá trị cao nghĩa là hiệu suất biến động mạnh theo thời gian — chiến lược có thể phụ thuộc vào điều kiện thị trường.",
                thresholds_vi: "Càng thấp = càng nhất quán",
            },
        ]
    }
}

/// Collapse the equity curve to one value per calendar day (last bar of each day),
/// and return the daily-equity series alongside an empirical `days_per_year`.
///
/// `days_per_year` = actual trading days observed / elapsed calendar years:
///   - US stocks → ~252 (weekdays only)
///   - Crypto    → ~365 (24/7)
///   - Both correct without any hardcoded constant
///
/// Using daily returns as the base unit for Sharpe/Sortino:
///   1. Makes M1 and H1 backtests directly comparable
///   2. Avoids the variance inflation from consecutive zero-return bars
///      (equity unchanged while no position is open)
fn aggregate_to_daily(portfolio: &Portfolio) -> (Vec<f64>, f64) {
    use std::collections::BTreeMap;

    if portfolio.equity_curve.is_empty() {
        return (vec![], 252.0);
    }

    // Key = calendar day (ms → day index), value = last equity of that day.
    let mut days: BTreeMap<i64, f64> = BTreeMap::new();
    for p in &portfolio.equity_curve {
        let day = p.timestamp / 86_400_000;
        days.insert(day, p.equity);
    }

    let mut daily_equity: Vec<f64> = days.values().copied().collect();
    // Prepend initial capital to daily equity curve to capture the returns of the first trading day.
    daily_equity.insert(0, portfolio.initial_capital);

    let n = daily_equity.len();
    if n < 2 {
        return (daily_equity, 252.0);
    }

    let first_day = *days.keys().next().unwrap() as f64;
    let last_day  = *days.keys().last().unwrap() as f64;
    // We add 1.0 to elapsed days to account for the starting day prepended at the beginning.
    let elapsed_years = (last_day - first_day + 1.0) / 365.25;  // day index units
    let days_per_year = if elapsed_years > 1e-6 {
        (n - 1) as f64 / elapsed_years
    } else {
        252.0
    };

    (daily_equity, days_per_year)
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::{
        order::Side,
        portfolio::{EquityPoint, Portfolio},
        trade::Trade,
    };

    fn make_trade(pnl: f64, pnl_pct: f64, entry_ts: i64, exit_ts: i64) -> Trade {
        Trade {
            symbol: "TEST".into(),
            side: Side::Buy,
            qty: 1.0,
            entry_price: 100.0,
            exit_price: 100.0 + pnl,
            entry_timestamp: entry_ts,
            exit_timestamp: exit_ts,
            pnl,
            pnl_pct,
            commission: 0.0,
            mae_pct: 0.0,
            mfe_pct: 0.0,
            bars_held: 0,
            exit_reason: alm_core::exit::ExitReason::Signal,
            regime_at_entry: None,
        }
    }

    fn make_portfolio(equity_values: &[(i64, f64)], trades: Vec<Trade>) -> Portfolio {
        let mut p = Portfolio::new(equity_values[0].1);
        p.equity_curve = equity_values
            .iter()
            .map(|&(ts, eq)| EquityPoint { timestamp: ts, equity: eq })
            .collect();
        p.trades = trades;
        p
    }

    #[test]
    fn report_empty_portfolio() {
        let p = make_portfolio(&[(0, 10_000.0)], vec![]);
        let r = BacktestReport::generate("test", "SYM", &p, 0.0);
        assert_eq!(r.total_trades, 0);
        assert_eq!(r.final_equity, 10_000.0);
        assert_eq!(r.total_return_pct, 0.0);
        assert_eq!(r.win_rate_pct, 0.0);
    }

    #[test]
    fn report_profitable() {
        let day_ms = 86_400_000i64;
        let p = make_portfolio(
            &[(0, 10_000.0), (day_ms * 100, 12_000.0)],
            vec![
                make_trade(500.0, 0.05, 0, day_ms * 30),
                make_trade(500.0, 0.05, day_ms * 40, day_ms * 70),
            ],
        );
        let r = BacktestReport::generate("strategy", "MBB", &p, 0.0);
        assert_eq!(r.strategy, "strategy");
        assert_eq!(r.symbol, "MBB");
        assert!((r.total_return_pct - 20.0).abs() < 0.01);
        assert_eq!(r.total_trades, 2);
        assert_eq!(r.win_rate_pct, 100.0);
        assert_eq!(r.profit_factor, f64::INFINITY);
        assert_eq!(r.max_consecutive_losses, 0);
    }

    #[test]
    fn report_losing() {
        let day_ms = 86_400_000i64;
        let p = make_portfolio(
            &[(0, 10_000.0), (day_ms * 50, 8_000.0)],
            vec![
                make_trade(-500.0, -0.05, 0, day_ms * 10),
                make_trade(-300.0, -0.03, day_ms * 20, day_ms * 30),
            ],
        );
        let r = BacktestReport::generate("loser", "XYZ", &p, 0.0);
        assert!(r.total_return_pct < 0.0);
        assert_eq!(r.win_rate_pct, 0.0);
        assert_eq!(r.profit_factor, 0.0);
        assert_eq!(r.max_consecutive_losses, 2);
        assert!(r.max_drawdown_pct > 0.0);
    }

    #[test]
    fn report_mixed_trades() {
        let day_ms = 86_400_000i64;
        let trades = vec![
            make_trade(200.0, 0.02, 0, day_ms),
            make_trade(-100.0, -0.01, day_ms * 2, day_ms * 3),
            make_trade(300.0, 0.03, day_ms * 4, day_ms * 5),
            make_trade(-50.0, -0.005, day_ms * 6, day_ms * 7),
            make_trade(-50.0, -0.005, day_ms * 8, day_ms * 9),
        ];
        let p = make_portfolio(&[(0, 10_000.0), (day_ms * 10, 10_300.0)], trades);
        let r = BacktestReport::generate("mixed", "T", &p, 0.0);
        assert_eq!(r.total_trades, 5);
        assert!((r.win_rate_pct - 40.0).abs() < 0.01);
        assert_eq!(r.max_consecutive_losses, 2);
        assert!(r.avg_trade_duration_hours > 0.0);
    }

    #[test]
    fn report_drawdown_accuracy() {
        // 10_000 → 15_000 → 9_000: max DD = (15000-9000)/15000 = 40%
        let day_ms = 86_400_000i64;
        let p = make_portfolio(
            &[(0, 10_000.0), (day_ms, 15_000.0), (day_ms * 2, 9_000.0), (day_ms * 3, 12_000.0)],
            vec![],
        );
        let r = BacktestReport::generate("dd_test", "T", &p, 0.0);
        assert!((r.max_drawdown_pct - 40.0).abs() < 0.01);
    }

    #[test]
    fn report_cagr_approx_one_year() {
        // +20% over ~1 year → CAGR ≈ 20%
        let one_year_ms = (365.25 * 24.0 * 3600.0 * 1000.0) as i64;
        let p = make_portfolio(&[(0, 10_000.0), (one_year_ms, 12_000.0)], vec![]);
        let r = BacktestReport::generate("cagr_test", "T", &p, 0.0);
        assert!((r.cagr_pct - 20.0).abs() < 0.1);
    }
}
