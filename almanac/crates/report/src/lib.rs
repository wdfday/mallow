//! # alm-report — Backtest Analytics Engine
//!
//! Computes performance metrics from a completed backtest (a [`Portfolio`] containing
//! an equity curve and a list of closed [`Trade`]s). The entry point is
//! [`BacktestReport::generate`]; everything else is a building block exposed for
//! downstream use.
//!
//! ---
//!
//! ## Architecture
//!
//! ```text
//! Portfolio { equity_curve, trades }
//!        │
//!        ▼
//! BacktestReport::generate()
//!        │
//!        ├── metrics::*          — pure functions, no state
//!        ├── BuyHoldBenchmark    — passive-reference comparison
//!        ├── monte_carlo::run()  — bootstrap robustness
//!        └── portfolio_analyze() — cross-symbol correlation / beta / alpha
//! ```
//!
//! ---
//!
//! ## Annualisation convention
//!
//! All ratio metrics (Sharpe, Sortino, volatility) use **daily returns** as the
//! base unit, regardless of the bar frequency of the backtest:
//!
//! - The equity curve is collapsed to one value per calendar day (last bar of each day).
//! - `days_per_year` is computed empirically from the equity-curve timestamps:
//!   `(trading_days_observed) / (elapsed_calendar_years)`.
//!   US stocks → ~252, crypto (24/7) → ~365 — no hardcoded constant.
//! - This makes M1 and H1 backtests directly comparable and avoids variance
//!   inflation from consecutive zero-return bars while no position is open.
//!
//! ---
//!
//! ## Metric Reference
//!
//! ### Return Metrics
//!
//! #### `total_return_pct`
//! Simple return over the full backtest period.
//! ```text
//! total_return = (final_equity - initial_capital) / initial_capital × 100
//! ```
//! No time-weighting — a 50% gain over 10 years looks identical to one over 10 days.
//! Use [`cagr_pct`] for time-normalised comparison.
//!
//! #### `cagr_pct` — Compound Annual Growth Rate
//! Geometric mean annual return. Preferred over simple return for period comparison.
//! ```text
//! CAGR = (final_equity / initial_capital)^(1 / duration_years) - 1   [× 100 for %]
//! ```
//! Duration is measured from the first to the last equity-curve timestamp (ms).
//!
//! #### `annualized_volatility_pct`
//! Standard deviation of daily returns scaled to annual units.
//! ```text
//! ann_vol = std_dev(daily_returns) × √(days_per_year) × 100
//! ```
//! Uses sample standard deviation (divides by n−1). Represents the ±1σ band of
//! annual returns under the normality assumption.
//!
//! ---
//!
//! ### Risk-Adjusted Return Ratios
//!
//! #### `sharpe_ratio` — Sharpe Ratio (William Sharpe, 1966 / 1994)
//! Excess return per unit of total volatility. The most widely cited risk-adjusted metric.
//! ```text
//! Sharpe = (mean(excess_daily) / std(excess_daily)) × √(days_per_year)
//! excess_daily[i] = daily_return[i] - risk_free_daily
//! risk_free_daily  = (1 + risk_free_annual)^(1/days_per_year) - 1
//! ```
//! **Interpretation:** > 1 = acceptable, > 2 = good, > 3 = excellent.
//! Punishes both upside and downside volatility equally — strategies with positive
//! skew are penalised unfairly. Use Sortino for asymmetric return profiles.
//!
//! #### `sortino_ratio` — Sortino Ratio (Frank Sortino & Lee Price, 1994)
//! Like Sharpe but penalises only **downside** deviation. Better for strategies
//! with significant positive skew (e.g. momentum, trend following).
//! ```text
//! Sortino = (mean(excess_daily) / downside_dev) × √(days_per_year)
//!
//! downside_dev = √( Σ min(excess[i], 0)² / n )
//! ```
//! Note: denominator uses **all n periods** (standard definition), not just the count
//! of negative periods. This avoids inflating Sortino when negative days are rare.
//! Returns `+∞` when there are no negative excess returns.
//!
//! #### `calmar_ratio` — Calmar Ratio (Terry Young, 1991)
//! Named after the newsletter *California Managed Accounts Reports*. Reward-to-
//! worst-drawdown ratio; common in managed futures and hedge fund evaluation.
//! ```text
//! Calmar = CAGR% / max_drawdown%
//! ```
//! **Interpretation:** > 0.5 = adequate, > 1 = good, > 3 = excellent.
//! Unlike Sharpe, Calmar uses the single worst realised loss rather than
//! all-period volatility — it captures tail risk more directly.
//!
//! ---
//!
//! ### Drawdown Metrics
//!
//! A **drawdown** at bar i is the percentage decline from the most recent equity peak:
//! ```text
//! DD[i] = (peak[i] - equity[i]) / peak[i]
//! ```
//!
//! #### `max_drawdown_pct`
//! The largest drawdown observed over the entire backtest. The primary risk metric
//! for practitioners: tells you the worst loss a live account would have experienced.
//! Reported as a positive percentage.
//!
//! #### `max_dd_duration_bars`
//! Number of bars between the equity peak that preceded the maximum drawdown and the
//! trough of that drawdown. Measures how *quickly* the drawdown reached its worst point.
//! Does **not** include recovery time.
//!
//! #### `avg_drawdown_pct`
//! Mean drawdown across all bars where a drawdown is active (DD > 0). Captures the
//! typical "pain level" of holding the strategy, as opposed to just the worst case.
//!
//! #### `ulcer_index` — Ulcer Index (Peter Martin & Byron McCann, 1987)
//! RMS of all drawdown depths expressed as percentages. Captures both depth *and*
//! duration of underwater periods — a long shallow drawdown scores higher than a
//! brief deep one of equal area.
//! ```text
//! UI = √( Σ DD[i]² / n )     where DD[i] is in %
//! ```
//! Lower is better. Used as the denominator in the Serenity Ratio.
//!
//! #### `serenity_ratio`
//! CAGR divided by the Ulcer Index. A risk-adjusted return measure that accounts
//! for both drawdown depth and duration — arguably more meaningful than Calmar for
//! long-running strategies.
//! ```text
//! Serenity = CAGR% / Ulcer Index
//! ```
//!
//! ---
//!
//! ### Trade-Level Statistics
//!
//! Computed directly from the list of closed trades, not from the equity curve.
//!
//! #### `win_rate_pct`
//! Fraction of trades with `pnl > 0`, expressed as a percentage.
//! ```text
//! win_rate = wins / total_trades × 100
//! ```
//! Meaningless in isolation — a 30% win rate is fine with a 3:1 payoff ratio.
//!
//! #### `profit_factor`
//! Ratio of gross profit to gross loss (both in currency units).
//! ```text
//! profit_factor = Σ pnl(winners) / Σ |pnl(losers)|
//! ```
//! > 1 = profitable overall. Returns `+∞` when there are no losing trades.
//! **Interpretation:** > 1.2 = acceptable, > 1.5 = good, > 2.0 = excellent.
//!
//! #### `expectancy`
//! Expected return per trade as a fraction of position size.
//! ```text
//! expectancy = win_rate × avg_win_pct - loss_rate × avg_loss_pct
//! ```
//! Positive expectancy is the minimum requirement for a viable strategy. Equivalent to
//! the statistical edge per trade. Can be thought of as "expected $ per $ risked".
//!
//! #### `payoff_ratio`
//! Average win size divided by average loss size (both positive percentages).
//! ```text
//! payoff_ratio = avg_win_pct / avg_loss_pct
//! ```
//! Together with win rate, fully determines expectancy. A payoff ratio of 2 with a
//! 40% win rate is breakeven; the same payoff with 45% is profitable.
//!
//! #### `breakeven_win_rate_pct`
//! The minimum win rate needed to be profitable at the current payoff ratio.
//! ```text
//! breakeven = 1 / (1 + payoff_ratio) × 100
//! ```
//! If actual win rate exceeds this, the strategy has positive expectancy.
//!
//! #### `avg_win_pct` / `avg_loss_pct`
//! Mean return of winning / losing trades, expressed as percentage of position size.
//! `avg_loss_pct` is stored as a positive number (absolute value of loss).
//!
//! #### `avg_trade_duration_hours`
//! Mean time between entry and exit across all trades, in hours.
//!
//! #### `max_consecutive_losses` / `max_consecutive_wins`
//! Longest unbroken streak of losing / winning trades. Important for position sizing:
//! a strategy with `max_consecutive_losses = 10` requires enough capital to survive
//! 10 consecutive max-loss trades.
//!
//! #### `largest_win_pct` / `largest_loss_pct`
//! Single-trade extremes as percentage of position size. The largest loss is reported
//! as a positive number. Identifies outlier trades that dominate the result.
//!
//! #### `gross_profit_usd` / `gross_loss_usd`
//! Total currency profit from winning trades / total currency loss from losing trades.
//! `profit_factor = gross_profit / gross_loss`.
//!
//! #### `avg_bars_held_winners` / `avg_bars_held_losers`
//! Average number of bars a trade was held open, split by outcome. Losers held longer
//! than winners signals "letting losers run" — a common behavioural bias.
//!
//! #### `trades_per_year`
//! Total trades divided by backtest duration in years. Relevant for statistical
//! significance: fewer than ~30 trades per year produces unreliable metrics.
//!
//! ---
//!
//! ### MAE / MFE
//!
//! **Maximum Adverse Excursion (MAE)** and **Maximum Favourable Excursion (MFE)**
//! (John Sweeney, *Campaign Trading*, 1996) measure the worst intra-trade loss and
//! the best intra-trade profit seen during each trade's lifetime.
//!
//! #### `avg_mae_pct`
//! Average MAE across all trades (as % of entry price). Measures typical worst-case
//! intra-trade exposure. Used to calibrate stop-loss placement: setting a stop tighter
//! than MAE cuts winners prematurely.
//!
//! #### `avg_mfe_pct`
//! Average MFE across all trades. Measures typical best-case intra-trade profit.
//! The gap between MFE and actual exit profit reveals how much gain is left on the table.
//!
//! #### `mfe_capture_ratio`
//! For winning trades: `mean(pnl_pct / mfe_pct)`. Fraction of peak intra-trade profit
//! that was actually captured. 0.7 means exits happen at 70% of the maximum move on
//! average. Capped at 1.0 per trade. Lower values → exits are too early on winners.
//!
//! #### `mae_mfe_ratio`
//! `avg_mae_pct / avg_mfe_pct`. Values > 1 mean typical adverse excursion exceeds
//! typical favourable excursion — the strategy spends more time losing intra-trade
//! than gaining.
//!
//! ---
//!
//! ### Advanced Risk Metrics
//!
//! #### `var_95` — Value at Risk (95%)
//! The 5th percentile of the daily-return distribution: the loss that is exceeded on
//! only 5% of trading days. Computed historically (empirical distribution, no
//! normality assumption).
//! ```text
//! VaR₉₅ = -percentile(daily_returns, 5%)   [reported as positive]
//! ```
//!
//! #### `cvar_95` — Conditional Value at Risk (Expected Shortfall)
//! Mean of the worst 5% of daily returns. Also called *Expected Shortfall* (ES).
//! CVaR is a **coherent risk measure** (Artzner et al., 1999); VaR is not. CVaR
//! captures the average severity of tail losses, not just the threshold.
//! ```text
//! CVaR₉₅ = -mean(daily_returns ≤ VaR₉₅ quantile)
//! ```
//!
//! #### `omega_ratio` — Omega Ratio (Shadwick & Keating, 2002)
//! Ratio of gains to losses relative to a threshold return (default: 0%).
//! Unlike Sharpe, Omega uses the full empirical distribution with no normality
//! assumption and accounts for all moments simultaneously.
//! ```text
//! Ω = Σ max(r - τ, 0) / Σ max(τ - r, 0)     τ = threshold (default 0)
//! ```
//! > 1 = more probability-weighted gain than loss above/below threshold.
//!
//! #### `tail_ratio`
//! Ratio of the 95th percentile return to the absolute value of the 5th percentile.
//! ```text
//! tail_ratio = |p95(daily_returns)| / |p5(daily_returns)|
//! ```
//! > 1 means right-tail gains are larger than left-tail losses — asymmetric upside.
//! Requires at least 20 observations.
//!
//! #### `recovery_factor`
//! Total return divided by maximum drawdown (both as raw fractions).
//! ```text
//! recovery_factor = total_return_fraction / max_drawdown_fraction
//! ```
//! Measures how much the strategy earned relative to its worst loss. > 1 means the
//! strategy earned back more than its maximum drawdown.
//!
//! ---
//!
//! ### Return Distribution Shape
//!
//! #### `skewness`
//! Sample skewness (third standardised moment) of the daily-return distribution.
//! ```text
//! γ₁ = (1/n) Σ ((rᵢ - μ) / σ)³
//! ```
//! Positive skew = occasional large gains with frequent small losses (good for
//! trend-following). Negative skew = frequent small gains with occasional large losses
//! (common in mean-reversion and short-volatility strategies — "picking up pennies").
//!
//! #### `excess_kurtosis`
//! Sample excess kurtosis (fourth standardised moment minus 3). Zero for a normal
//! distribution; positive for fat tails (leptokurtic).
//! ```text
//! γ₂ = (1/n) Σ ((rᵢ - μ) / σ)⁴ - 3
//! ```
//! Most financial return series have positive excess kurtosis — extreme events occur
//! more frequently than a normal distribution predicts.
//!
//! #### `psr` — Probabilistic Sharpe Ratio (López de Prado, 2012)
//! Probability that the true (population) Sharpe Ratio is greater than a benchmark,
//! accounting for the non-normality of returns via skewness and kurtosis.
//! ```text
//! PSR(SR*) = Φ( (SR_hat - SR*) / √(Var(SR_hat)) )
//!
//! Var(SR_hat) = (1 - γ₁·SR_hat + (γ₂-1)/4 · SR_hat²) / (T - 1)
//! ```
//! Where Φ is the standard normal CDF. Reported as P(SR > 0) by default.
//! **Use:** PSR > 0.95 provides statistical confidence that the strategy has a
//! genuine edge, not just in-sample luck.
//!
//! #### `dsr` — Deflated Sharpe Ratio (López de Prado, 2014)
//! PSR corrected for the **multiple-testing bias** introduced by evaluating many
//! parameter combinations. If you tested 100 parameter sets, the best result is
//! partially a product of luck.
//! ```text
//! DSR = PSR( E[max SR | H₀, n_trials] )
//!
//! E[max SR] ≈ σ_SR × [ (1-γ_EM) Φ⁻¹(1 - 1/n) + γ_EM Φ⁻¹(1 - 1/(n·e)) ]
//! ```
//! Where γ_EM ≈ 0.5772 (Euler–Mascheroni constant). DSR < PSR whenever n_trials > 1.
//! Available via `dsr(returns, n_trials)` in `metrics`.
//!
//! ---
//!
//! ### Position Sizing Guidance
//!
//! #### `sqn` — System Quality Number (Van Tharp, *Trade Your Way to Financial Freedom*)
//! Combines expectancy and consistency into a single score.
//! ```text
//! SQN = √(min(n, 100)) × mean(R) / std(R)
//! ```
//! Where R is the pnl_pct of each trade (used as an R-multiple proxy).
//! n is capped at 100 per Van Tharp's original definition to prevent SQN from
//! inflating arbitrarily with more trades.
//!
//! **Score guide:** < 1.6 = poor, 1.6–1.9 = below average, 2.0–2.4 = average,
//! 2.5–2.9 = good, 3.0–5.0 = excellent, > 5.0 = holy-grail territory.
//!
//! #### `kelly_pct` — Kelly Criterion (John L. Kelly Jr., 1956)
//! Optimal fraction of capital to risk per trade to maximise long-run geometric growth.
//! ```text
//! K = W - (1 - W) / R
//! ```
//! Where W = win_rate [0,1] and R = avg_win_pct / avg_loss_pct.
//! Clamped to [−1, 1]; negative K means the edge is against you.
//! **Practical note:** Full Kelly is aggressive. Most practitioners use half-Kelly
//! (K/2) or quarter-Kelly to reduce variance. The value here is full Kelly.
//!
//! ---
//!
//! ### Long / Short Breakdown
//!
//! `long_stats` and `short_stats` are [`DirectionStats`] structs containing
//! `count`, `win_rate`, `profit_factor`, `avg_win_pct`, `avg_loss_pct`, and
//! `expectancy` computed independently for long (Buy) and short (Sell) trades.
//! Useful for detecting directional bias — a strategy that works only on longs
//! in a bull market is fragile.
//!
//! ---
//!
//! ### Temporal Return Breakdown
//!
//! #### `monthly_returns`
//! `Vec<(year, month 1–12, return_pct)>` — return of each calendar month derived
//! from the equity curve (last equity point of the month). The first month uses the
//! initial equity as its start.
//!
//! #### `yearly_returns`
//! Derived by compounding monthly returns within each calendar year:
//! ```text
//! annual_return = Π (1 + monthly_return/100) - 1   [× 100 for %]
//! ```
//!
//! ---
//!
//! ### Rolling Metrics
//!
//! Computed bar-by-bar for use as chart overlays.
//!
//! #### `rolling_sharpe`
//! 30-bar rolling Sharpe ratio at each equity-curve point. Zero-padded during warmup.
//! Useful for visualising regime changes in strategy performance.
//!
//! #### `rolling_drawdown`
//! Drawdown percentage at each bar (distance from running peak). Same length as equity curve.
//!
//! #### `rolling_sharpe_std`
//! Standard deviation of the 30-bar rolling Sharpe series. Measures **Sharpe stability**:
//! a strategy with high mean Sharpe but high `rolling_sharpe_std` performs inconsistently
//! across time — regime-dependent.
//!
//! ---
//!
//! ### Exit Reason Breakdown
//!
//! [`ExitReasonBreakdown`] counts how many trades were closed by each mechanism:
//! `signal`, `stop_loss`, `take_profit`, `trailing_stop`, `atr_stop`, `atr_target`,
//! `max_bars`, `end_of_data`.
//!
//! A high `end_of_data` count indicates trades that were force-closed at backtest
//! end — their PnL may be unrealised and should be discounted.
//!
//! ---
//!
//! ### Buy-and-Hold Benchmark ([`BuyHoldBenchmark`])
//!
//! Buys at the first close price, holds to the last. Computed directly from a
//! close-price series (no commission, no position sizing). Reports the same scalar
//! metrics as `BacktestReport` for direct comparison.
//!
//! A strategy whose Sharpe < B&H Sharpe is destroying alpha; even if profitable,
//! a leveraged index ETF would have done better with less risk.
//!
//! ---
//!
//! ### Monte Carlo Simulation ([`monte_carlo`])
//!
//! Bootstrap resampling of trade PnL to estimate the distribution of outcomes under
//! random ordering. Answers: "how bad could this have been if the same trades arrived
//! in a different order?"
//!
//! **Method:**
//! 1. Sample `n_trades` trade returns **with replacement** from the historical list.
//! 2. Simulate a compound equity curve from `initial_capital`.
//! 3. Record whether equity ever crossed `ruin_threshold × initial_capital`.
//! 4. Repeat `n_iter` times (default 1 000).
//!
//! **Outputs:** `ruin_probability` + percentile final equities (p5/p10/p25/p50/p75/p90/p95)
//! + full percentile equity *curves* for each percentile (useful for fan-chart visualisation).
//!
//! **Limitations:** Assumes trade returns are i.i.d. — does not model serial correlation,
//! regime changes, or leverage effects.
//!
//! ---
//!
//! ### Portfolio Analytics ([`portfolio_analyze`])
//!
//! Cross-symbol statistics for multi-asset portfolios.
//!
//! #### Correlation matrix
//! Pearson correlation of daily equity-curve returns between every pair of symbols.
//! ```text
//! ρ(X,Y) = Cov(X,Y) / (σ_X × σ_Y)
//! ```
//! High correlation (> 0.8) between positions reduces diversification benefit.
//!
//! #### Beta
//! Sensitivity of each symbol's returns to the benchmark (e.g. buy-and-hold of the
//! primary asset).
//! ```text
//! β = Cov(symbol, benchmark) / Var(benchmark)
//! ```
//! β > 1 = amplifies benchmark moves; β < 0 = hedge.
//!
//! #### Alpha (annualised)
//! Excess return not explained by beta exposure.
//! ```text
//! α = mean(symbol_daily) - β × mean(benchmark_daily)
//! α_annual = α × days_per_year
//! ```
//! Positive alpha = genuine skill beyond passive exposure.
//!
//! #### Diversification ratio
//! ```text
//! DR = Σ wᵢ × σᵢ / σ_portfolio     (equal weights)
//! ```
//! DR > 1 means the portfolio benefits from diversification. DR = 1 means all
//! assets move perfectly together.

pub mod benchmark;
pub mod metrics;
pub mod monte_carlo;
pub mod portfolio_analytics;
pub mod report;

pub use benchmark::BuyHoldBenchmark;
pub use metrics::{DirectionStats, dsr, psr, normal_cdf, probit, skewness, excess_kurtosis};
pub use monte_carlo::{run as monte_carlo, MonteCarloConfig, MonteCarloResult};
pub use portfolio_analytics::{analyze as portfolio_analyze, PortfolioAnalytics};
pub use report::BacktestReport;
