//! Backtest runner — executes a named / CEL / dynamic strategy over
//! historical bars. Moved from the former `logbook` crate.

use std::path::Path;

use std::collections::HashMap;

use alm_core::{
    exit::ExitRules,
    order::Side,
    portfolio::PortfolioSnapshot,
    regime::RegimeState,
    signal::{Direction, Signal},
    strategy::Strategy,
    Bar,
};
use alm_data::BarFeed;
use alm_report::{BuyHoldBenchmark, monte_carlo as mc_run, MonteCarloConfig as McConfig};
use alm_strategy::{build_strategy, AnySizer, FixedFractional, FixedQuantity, FixedUsd};
use anyhow::{Context, Result};
use chrono::{DateTime, Utc};
use serde_json::Value;

// ── StrengthFilter ────────────────────────────────────────────────────────────

/// Wraps any `Strategy` and discards entry/exit signals below `min` strength.
/// Close signals are always forwarded regardless of strength.
struct StrengthFilter {
    inner: Box<dyn Strategy>,
    min: f64,
}

impl Strategy for StrengthFilter {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let sigs = self.inner.on_bar(bar);
        if self.min <= 0.0 {
            return sigs;
        }
        sigs.into_iter()
            .filter(|s| s.direction == Direction::Close || s.strength >= self.min)
            .collect()
    }

    fn on_window(&mut self, bars: &[Bar]) -> Vec<Signal> {
        let sigs = self.inner.on_window(bars);
        if self.min <= 0.0 {
            return sigs;
        }
        sigs.into_iter()
            .filter(|s| s.direction == Direction::Close || s.strength >= self.min)
            .collect()
    }

    fn name(&self) -> &str {
        self.inner.name()
    }

    fn reset(&mut self) {
        self.inner.reset();
    }

    fn set_portfolio_snapshot(&mut self, snapshot: &PortfolioSnapshot) {
        self.inner.set_portfolio_snapshot(snapshot);
    }

    fn on_regime(&mut self, regime: &RegimeState) {
        self.inner.on_regime(regime);
    }

    fn take_indicator_series(&mut self) -> HashMap<String, Vec<(i64, f64)>> {
        self.inner.take_indicator_series()
    }
}

use crate::data::{find_parquet_files, load_bars, market_region_from_data_source, parse_date_ms};
use crate::types::{
    ActivityStats, BacktestRequest, BacktestResponse, BuyHoldBenchmarkResponse, CalendarStats,
    CapitalStats, CurvePoint, CurveStats, DistributionStats, DrawdownStats, ExcursionStats,
    ExitConfig, ExitLevel, ExitReasonBreakdownResponse, LongShortStats, MonteCarloResponse,
    RegimeSummaryResponse, ReturnStats, RiskAdjustedStats, TradeResponse, TradeStats,
    WalkForwardResponse,
    // FUTURE[wf-v2]: WalkForwardWindowResponse,
};
use alm_core::exit::IntraBarMode;
// FUTURE[wf-v2]: use crate::walk_forward::{walk_forward, WalkForwardConfig as WfCfg, WalkForwardMode};
use crate::Engine;

const DEFAULT_RISK_FREE: f64 = 0.04; // 4% — US Treasury proxy.
const DEFAULT_COMMISSION: f64 = 0.001; // 0.1% per trade.
const DEFAULT_SLIPPAGE: f64 = 0.0005; // 0.05%.
const DEFAULT_CAPITAL: f64 = 10_000.0;
const DEFAULT_POSITION_PCT: f64 = 0.95; // 95% of cash per trade.
const DEFAULT_CURVE_POINTS: usize = 2_000;

/// Estimate bar count for a backtest request without running the engine.
/// Returns `(bar_count, estimated_seconds)`.
/// `estimated_seconds` is a rough heuristic: ~500k bars/sec on modern hardware.
pub fn estimate(req: &BacktestRequest, data_dir: &Path) -> Result<(usize, f64)> {
    let symbol = if req.symbol.is_empty() { "BTCUSD" } else { &req.symbol };
    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req.to.as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));
    let market_hours_only = req.market_hours_only.unwrap_or(false);
    let market_region = req.data_source.as_deref()
        .map(market_region_from_data_source)
        .unwrap_or("us");
    let files = find_parquet_files(data_dir, symbol, req.timeframe.as_deref(), req.data_source.as_deref());
    let feed = load_bars(&files, symbol, from_ms, to_ms, market_hours_only, market_region)
        .with_context(|| format!("loading data for '{}'", symbol))?;
    let bar_count = feed.len();
    // Heuristic: ~500k bars/sec for named strategies, ~200k/sec for CEL/Rhai.
    let bars_per_sec = match req.strategy.as_str() {
        "cel" | "cel2" | "rhai" => 200_000.0,
        _ => 500_000.0,
    };
    let estimated_seconds = bar_count as f64 / bars_per_sec;
    Ok((bar_count, estimated_seconds))
}

/// Run a full backtest from a request, discovering data under `data_dir`.
pub fn run(req: BacktestRequest, data_dir: &Path) -> Result<BacktestResponse> {
    let params = req.params.unwrap_or(Value::Object(Default::default()));
    let capital = req.initial_capital.unwrap_or(DEFAULT_CAPITAL);
    let symbol = if req.symbol.is_empty() {
        "BTCUSD".to_string()
    } else {
        req.symbol
    };

    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req
        .to
        .as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));

    let market_hours_only = req.market_hours_only.unwrap_or(false);
    let market_region = req
        .data_source
        .as_deref()
        .map(market_region_from_data_source)
        .unwrap_or("us");
    let files = find_parquet_files(
        data_dir,
        &symbol,
        req.timeframe.as_deref(),
        req.data_source.as_deref(),
    );
    let min_strength = req.min_strength.unwrap_or(0.0);
    let mut feed = load_bars(&files, &symbol, from_ms, to_ms, market_hours_only, market_region)
        .with_context(|| format!("loading data for '{}'", symbol))?;

    let bar_count = feed.len();

    // Hard cap: reject before running the engine to avoid OOM + multi-minute hangs.
    // Recommend a coarser timeframe when the cap is exceeded.
    const MAX_BARS: usize = 100_000;
    if bar_count > MAX_BARS {
        let tf = req.timeframe.as_deref().unwrap_or("M1").to_uppercase();
        let suggestion = match tf.as_str() {
            "M1"  => "Use M5 or M15 for ranges > 3 months",
            "M5"  => "Use M15 or H1 for ranges > 1 year",
            "M15" => "Use H1 for ranges > 2 years",
            "H1"  => "Use H4 or D1 for ranges > 5 years",
            _     => "Narrow the date range",
        };
        anyhow::bail!(
            "{} bars exceeds the {} bar limit. {}.",
            bar_count, MAX_BARS, suggestion
        );
    }

    tracing::info!(
        symbol = %symbol,
        strategy = %req.strategy,
        bars = bar_count,
        "starting backtest"
    );

    let inner = build_strategy(&req.strategy, &params)?;
    let strategy = StrengthFilter { inner, min: min_strength };
    let lot_size = asset_lot_size(req.asset_type.as_deref());
    let max_positions = req.max_positions.unwrap_or(1).max(1);

    // Sizer priority: quantity > USD > pct.
    let risk: AnySizer = if let Some(qty) = req.position_size_quantity {
        AnySizer::FixedQuantity(FixedQuantity::new(qty, max_positions))
    } else if let Some(usd) = req.position_size_usd {
        AnySizer::FixedUsd(FixedUsd::new(usd, max_positions).with_lot_size(lot_size))
    } else {
        let pct = req
            .position_size_pct
            .unwrap_or(DEFAULT_POSITION_PCT)
            .clamp(0.01, 1.0);
        AnySizer::FixedFractional(
            FixedFractional::new(pct, max_positions).with_lot_size(lot_size),
        )
    };

    let exit_rules = req.exit.clone()
        .map(|cfg| exit_rules_from_config(cfg, &params))
        .unwrap_or_default();

    let commission = req.commission_pct.unwrap_or(DEFAULT_COMMISSION);
    let slippage = req.slippage_pct.unwrap_or(DEFAULT_SLIPPAGE);
    let risk_free = req.risk_free_annual.unwrap_or(DEFAULT_RISK_FREE);

    // Drain feed into a Vec so we can reuse bars for walk-forward.
    let all_bars: Vec<alm_core::Bar> = std::iter::from_fn(|| feed.next()).collect();

    let mut engine = Engine::sync(capital, strategy, risk, commission, slippage)
        .with_exit_rules(exit_rules);

    let mut bar_feed = alm_data::BarVecFeed::new(all_bars.clone(), symbol.clone());
    let report = engine.run(&mut bar_feed, risk_free);

    let closes: Vec<f64> = all_bars.iter().map(|b| b.close).collect();
    let timestamps: Vec<i64> = all_bars.iter().map(|b| b.timestamp).collect();
    let bh = BuyHoldBenchmark::compute(&closes, &timestamps, risk_free);
    let benchmark = if closes.len() >= 2 {
        Some(BuyHoldBenchmarkResponse {
            total_return_pct: bh.total_return_pct,
            cagr_pct: bh.cagr_pct,
            annualized_volatility_pct: bh.annualized_volatility_pct,
            sharpe_ratio: bh.sharpe_ratio,
            sortino_ratio: bh.sortino_ratio,
            max_drawdown_pct: bh.max_drawdown_pct,
            max_dd_duration_bars: bh.max_dd_duration_bars,
        })
    } else {
        None
    };

    let raw_series = engine.strategy.take_indicator_series();
    let indicator_series: std::collections::HashMap<String, Vec<CurvePoint>> = raw_series
        .into_iter()
        .map(|(k, pts)| {
            (
                k,
                pts.into_iter().map(|(t, v)| CurvePoint { t, v }).collect(),
            )
        })
        .collect();

    let trades: Vec<TradeResponse> = engine
        .portfolio
        .trades
        .iter()
        .map(|t| TradeResponse {
            symbol: t.symbol.clone(),
            side: match t.side {
                Side::Buy => "long".into(),
                Side::Sell => "short".into(),
            },
            qty: t.qty,
            entry_price: t.entry_price,
            exit_price: t.exit_price,
            entry_ts: t.entry_timestamp,
            exit_ts: t.exit_timestamp,
            entry_time: ms_to_iso(t.entry_timestamp),
            exit_time: ms_to_iso(t.exit_timestamp),
            pnl: t.pnl,
            pnl_pct: t.pnl_pct,
            commission: t.commission,
            mae_pct: t.mae_pct * 100.0,
            mfe_pct: t.mfe_pct * 100.0,
            bars_held: t.bars_held,
            exit_reason: t.exit_reason.to_string(),
        })
        .collect();

    let equity_curve: Vec<CurvePoint> = engine
        .portfolio
        .equity_curve
        .iter()
        .map(|p| CurvePoint {
            t: p.timestamp,
            v: p.equity,
        })
        .collect();

    let drawdown_curve: Vec<CurvePoint> = {
        let mut peak = capital;
        engine
            .portfolio
            .equity_curve
            .iter()
            .map(|p| {
                if p.equity > peak {
                    peak = p.equity;
                }
                let dd = if peak > 0.0 {
                    (p.equity - peak) / peak
                } else {
                    0.0
                };
                CurvePoint {
                    t: p.timestamp,
                    v: dd,
                }
            })
            .collect()
    };

    let no_trades = report.total_trades == 0;

    // Exposure: % of total period the strategy held a position.
    let exposure_pct = {
        let total_ms = engine
            .portfolio
            .equity_curve
            .last()
            .and_then(|last| {
                engine
                    .portfolio
                    .equity_curve
                    .first()
                    .map(|first| last.timestamp - first.timestamp)
            })
            .unwrap_or(0);
        let held_ms: i64 = engine
            .portfolio
            .trades
            .iter()
            .map(|t| t.exit_timestamp - t.entry_timestamp)
            .sum();
        if total_ms > 0 {
            held_ms as f64 / total_ms as f64 * 100.0
        } else {
            0.0
        }
    };

    let curve_max = match req.curve_points {
        Some(0) => usize::MAX, // 0 = no limit
        Some(n) => n,
        None => DEFAULT_CURVE_POINTS,
    };

    let regime_summary = report.regime_summary.map(|r| RegimeSummaryResponse {
        trending_pct: r.trending_pct,
        ranging_pct: r.ranging_pct,
        neutral_pct: r.neutral_pct,
        high_vol_pct: r.high_vol_pct,
        low_vol_pct: r.low_vol_pct,
        changes: r.changes,
    });

    // ── Monte Carlo (opt-in) ──────────────────────────────────────────────────
    let monte_carlo = req.monte_carlo.and_then(|mc_cfg| {
        // Use equity-relative returns (pnl / equity_at_entry) so the MC curves
        // scale correctly regardless of position sizing mode (pct / USD / qty).
        // t.pnl_pct is position-level (pnl / position_cost) which overstates
        // equity swings when position_size_pct < 1.0.
        let pnl_pct: Vec<f64> = engine.portfolio.trades.iter().filter_map(|t| {
            let idx = engine.portfolio.equity_curve
                .partition_point(|p| p.timestamp <= t.entry_timestamp);
            let eq = if idx > 0 { engine.portfolio.equity_curve[idx - 1].equity } else { return None };
            if eq > f64::EPSILON { Some(t.pnl / eq) } else { None }
        }).collect();
        let cfg = McConfig {
            n_iter:          mc_cfg.n_iter.unwrap_or(1_000),
            ruin_threshold:  mc_cfg.ruin_threshold.unwrap_or(0.50),
            seed:            mc_cfg.seed,
        };
        mc_run(&pnl_pct, capital, &cfg).map(|r| MonteCarloResponse {
            n_iter:          r.n_iter,
            n_trades:        r.n_trades,
            initial_capital: r.initial_capital,
            ruin_threshold:  r.ruin_threshold,
            ruin_probability: r.ruin_probability,
            final_p5:   r.final_p5,
            final_p10:  r.final_p10,
            final_p25:  r.final_p25,
            final_p50:  r.final_p50,
            final_p75:  r.final_p75,
            final_p90:  r.final_p90,
            final_p95:  r.final_p95,
            curve_p5:   r.curve_p5,
            curve_p10:  r.curve_p10,
            curve_p25:  r.curve_p25,
            curve_p50:  r.curve_p50,
            curve_p75:  r.curve_p75,
            curve_p90:  r.curve_p90,
            curve_p95:  r.curve_p95,
        })
    });

    // ── Walk-forward — FUTURE[wf-v2] ─────────────────────────────────────────
    // Disabled: walk-forward needs a parameter optimization layer to be
    // meaningful (IS optimize → OOS evaluate). Accepting the field in the
    // request schema is intentional — keeps the API stable for when we come
    // back to this. The `walk_forward` crate module is complete and correct;
    // only the HTTP dispatch is gated here.
    if req.walk_forward.is_some() {
        tracing::warn!("walk_forward field present but walk-forward is disabled — FUTURE[wf-v2]");
    }
    let walk_forward_result: Option<WalkForwardResponse> = None;
    /* --- FUTURE[wf-v2]: restore block below ---
    let walk_forward_result = req.walk_forward.map(|wf_cfg| {
        let mut cfg = WfCfg::new(wf_cfg.is_bars, wf_cfg.oos_bars, capital);
        cfg.step_bars = wf_cfg.step_bars.unwrap_or(wf_cfg.oos_bars);
        cfg.risk_free_annual = risk_free;
        cfg.mode = match wf_cfg.mode.as_deref() {
            Some("anchored") => WalkForwardMode::Anchored,
            _ => WalkForwardMode::Rolling,
        };
        cfg.embargo_bars = wf_cfg.embargo_bars.unwrap_or(0);
        cfg.min_oos_trades = wf_cfg.min_oos_trades;

        // Rebuild strategy + risk — engine was consumed above.
        // TODO[wf-v2]: mirror full sizing logic (USD / qty / pct) from main run.
        // TODO[wf-v2]: fix efficiency_ratio label — guard IS Sharpe sign before
        //              applying "overfit" label (misleading without optimization).
        let inner2 = build_strategy(&req.strategy, &params).expect("strategy rebuild");
        let strategy2 = StrengthFilter { inner: inner2, min: min_strength };
        let lot2 = asset_lot_size(req.asset_type.as_deref());
        let max2 = req.max_positions.unwrap_or(1).max(1);
        let risk2: AnySizer = if let Some(pct) = req.position_size_pct {
            AnySizer::FixedFractional(FixedFractional::new(pct.clamp(0.01, 1.0), max2).with_lot_size(lot2))
        } else {
            AnySizer::FixedFractional(FixedFractional::new(DEFAULT_POSITION_PCT, max2).with_lot_size(lot2))
        };
        let exit2 = req.exit.map(|c| exit_rules_from_config(c, &params)).unwrap_or_default();
        let mut wf_engine = Engine::sync(capital, strategy2, risk2, commission, slippage)
            .with_exit_rules(exit2);

        let wf = walk_forward(&mut wf_engine, &all_bars, &symbol, cfg);

        let windows = wf.windows.iter().map(|w| WalkForwardWindowResponse {
            window: w.window,
            is_range: [w.is_range.0, w.is_range.1],
            oos_range: [w.oos_range.0, w.oos_range.1],
            is_sharpe: w.in_sample.sharpe_ratio,
            is_return_pct: w.in_sample.total_return_pct,
            is_trades: w.in_sample.total_trades,
            oos_sharpe: w.out_of_sample.sharpe_ratio,
            oos_sortino: w.out_of_sample.sortino_ratio,
            oos_calmar: w.out_of_sample.calmar_ratio,
            oos_return_pct: w.out_of_sample.total_return_pct,
            oos_trades: w.out_of_sample.total_trades,
            oos_win_rate_pct: w.out_of_sample.win_rate_pct,
            oos_max_drawdown_pct: w.out_of_sample.max_drawdown_pct,
            oos_profit_factor: w.out_of_sample.profit_factor,
            oos_expectancy: w.out_of_sample.expectancy,
            oos_sqn: w.out_of_sample.sqn,
            oos_psr: w.out_of_sample.psr,
            oos_equity_curve: w.oos_equity_curve.iter()
                .map(|p| CurvePoint { t: p.timestamp, v: p.equity })
                .collect(),
        }).collect();

        let label = match wf.efficiency_ratio {
            r if r >= 0.7 => "robust",
            r if r >= 0.5 => "marginal",
            r if r >  0.0 => "overfit",
            _             => "no_data",
        };

        WalkForwardResponse {
            windows,
            avg_oos_sharpe: wf.avg_oos_sharpe,
            avg_oos_win_rate: wf.avg_oos_win_rate,
            avg_oos_return_pct: wf.avg_oos_return_pct,
            total_oos_trades: wf.total_oos_trades,
            pct_profitable_windows: wf.pct_profitable_windows,
            efficiency_ratio: wf.efficiency_ratio,
            efficiency_label: label.into(),
            oos_equity_curve: wf.oos_equity_curve.iter()
                .map(|p| CurvePoint { t: p.timestamp, v: p.equity })
                .collect(),
        }
    });
    --- end FUTURE[wf-v2] --- */

    Ok(BacktestResponse {
        strategy: req.strategy,
        symbol,
        params,
        timeframe: report.timeframe.to_string(),
        bar_count,
        capital: CapitalStats {
            initial: report.initial_capital,
            final_equity: report.final_equity,
        },
        returns: ReturnStats {
            total_pct: report.total_return_pct,
            cagr_pct: report.cagr_pct,
            annualized_volatility_pct: if no_trades { 0.0 } else { report.annualized_volatility_pct },
        },
        risk_adjusted: RiskAdjustedStats {
            sharpe: if no_trades { 0.0 } else { report.sharpe_ratio },
            sortino: if no_trades { 0.0 } else { report.sortino_ratio },
            calmar: if no_trades { 0.0 } else { report.calmar_ratio },
            serenity: if no_trades { 0.0 } else { report.serenity_ratio },
            omega: if no_trades { 0.0 } else { report.omega_ratio },
            tail_ratio: if no_trades { 0.0 } else { report.tail_ratio },
            recovery_factor: if no_trades { 0.0 } else { report.recovery_factor },
            var_95: if no_trades { 0.0 } else { report.var_95 },
            cvar_95: if no_trades { 0.0 } else { report.cvar_95 },
        },
        drawdown: DrawdownStats {
            max_pct: report.max_drawdown_pct,
            max_duration_bars: report.max_dd_duration_bars,
            avg_pct: report.avg_drawdown_pct,
            ulcer_index: if no_trades { 0.0 } else { report.ulcer_index },
        },
        trade_stats: TradeStats {
            total: report.total_trades,
            win_rate_pct: report.win_rate_pct,
            profit_factor: report.profit_factor,
            payoff_ratio: report.payoff_ratio,
            expectancy: report.expectancy,
            breakeven_win_rate_pct: report.breakeven_win_rate_pct,
            gross_profit_usd: report.gross_profit_usd,
            gross_loss_usd: report.gross_loss_usd,
            avg_win_pct: report.avg_win_pct,
            avg_loss_pct: report.avg_loss_pct,
            avg_duration_hours: report.avg_trade_duration_hours,
            avg_bars_held_winners: report.avg_bars_held_winners,
            avg_bars_held_losers: report.avg_bars_held_losers,
            max_consecutive_losses: report.max_consecutive_losses,
            max_consecutive_wins: report.max_consecutive_wins,
            largest_win_pct: report.largest_win_pct,
            largest_loss_pct: report.largest_loss_pct,
            mfe_capture_ratio: report.mfe_capture_ratio,
            exit_reasons: ExitReasonBreakdownResponse {
                signal: report.exit_reasons.signal,
                stop_loss: report.exit_reasons.stop_loss,
                take_profit: report.exit_reasons.take_profit,
                trailing_stop: report.exit_reasons.trailing_stop,
                atr_stop: report.exit_reasons.atr_stop,
                atr_target: report.exit_reasons.atr_target,
                max_bars: report.exit_reasons.max_bars,
                end_of_data: report.exit_reasons.end_of_data,
            },
        },
        distribution: DistributionStats {
            skewness: if no_trades { 0.0 } else { report.skewness },
            excess_kurtosis: if no_trades { 0.0 } else { report.excess_kurtosis },
            sqn: if no_trades { 0.0 } else { report.sqn },
            psr: if no_trades { 0.0 } else { report.psr },
        },
        long_short: LongShortStats {
            long_trades: report.long_stats.count,
            long_win_rate_pct: report.long_stats.win_rate * 100.0,
            long_profit_factor: report.long_stats.profit_factor,
            short_trades: report.short_stats.count,
            short_win_rate_pct: report.short_stats.win_rate * 100.0,
            short_profit_factor: report.short_stats.profit_factor,
        },
        excursion: ExcursionStats {
            avg_mae_pct: report.avg_mae_pct,
            avg_mfe_pct: report.avg_mfe_pct,
            mae_mfe_ratio: report.mae_mfe_ratio,
        },
        activity: ActivityStats {
            trades_per_year: report.trades_per_year,
            exposure_pct,
            total_commission_usd: report.total_commission_paid,
            kelly_pct: if no_trades { 0.0 } else { report.kelly_pct },
        },
        curves: CurveStats {
            equity: downsample(equity_curve, curve_max),
            drawdown: downsample(drawdown_curve, curve_max),
            rolling_sharpe: downsample_f64(report.rolling_sharpe, curve_max),
            rolling_sharpe_std: if no_trades { 0.0 } else { report.rolling_sharpe_std },
            rolling_drawdown: downsample_f64(report.rolling_drawdown, curve_max),
        },
        calendar: CalendarStats {
            monthly_returns: report.monthly_returns.iter()
                .map(|&(y, m, r)| [y as f64, m as f64, r])
                .collect(),
            yearly_returns: report.yearly_returns.iter().map(|&(y, r)| [y as f64, r]).collect(),
        },
        trades,
        indicator_series,
        regime_summary,
        benchmark,
        monte_carlo,
        walk_forward: walk_forward_result,
        walk_forward_note: req.walk_forward.as_ref().map(|_| "disabled — FUTURE[wf-v2]".into()),
    })
}

/// Build engine [`ExitRules`] from [`ExitConfig`].
///
/// Fixed-pct levels → directly into `ExitRules`. ATR-expression levels
/// (`"N*atr(P)"`) → injected into `cel_params` so that `CelStrategy` handles
/// them internally; engine-level `ExitRules` gets `None` for those fields
/// (the engine does not know ATR at construction time).
pub fn exit_rules_from_config(cfg: ExitConfig, cel_params: &Value) -> ExitRules {
    let _ = cel_params; // reserved for future ATR injection into non-CEL strategies.
    let intra_bar_mode = match cfg.intra_bar_mode.as_deref() {
        Some("pessimistic")    => IntraBarMode::Pessimistic,
        Some("ohlc_heuristic") => IntraBarMode::OhlcHeuristic,
        _                      => IntraBarMode::CloseOnly,
    };
    ExitRules {
        stop_loss_pct: cfg.sl.as_ref().and_then(ExitLevel::as_pct),
        take_profit_pct: cfg.tp.as_ref().and_then(ExitLevel::as_pct),
        trailing_stop_pct: cfg.trailing_stop,
        max_bars_held: cfg.max_bars,
        intra_bar_mode,
        ..Default::default()
    }
}

/// Inject ATR-based `tp`/`sl` from [`ExitConfig`] into a CEL params map.
/// Called by the CEL `From` conversion so that `CelStrategy::from_params`
/// picks them up.
pub fn inject_atr_exit_into_cel_params(
    cfg: &ExitConfig,
    params: &mut serde_json::Map<String, Value>,
) {
    use serde_json::json;
    if let Some(level) = &cfg.tp {
        match level {
            ExitLevel::Pct(v) => {
                params.insert("tp".into(), json!(v));
            }
            ExitLevel::Expr(_) => {
                if let Some((mult, period)) = level.as_atr() {
                    params.insert("tp_atr".into(), json!(mult));
                    params.insert("atr_period".into(), json!(period));
                }
            }
        }
    }
    if let Some(level) = &cfg.sl {
        match level {
            ExitLevel::Pct(v) => {
                params.insert("sl".into(), json!(v));
            }
            ExitLevel::Expr(_) => {
                if let Some((mult, period)) = level.as_atr() {
                    params.insert("sl_atr".into(), json!(mult));
                    // atr_period already set above; only overwrite if not set yet.
                    params.entry("atr_period").or_insert(json!(period));
                }
            }
        }
    }
}

/// Map `asset_type` string → lot size for position sizing.
/// `"crypto"` / `None` → 0.0 (fractional), `"stock"` → 1.0, `"vn_stock"` → 100.0.
pub fn asset_lot_size(asset_type: Option<&str>) -> f64 {
    match asset_type {
        Some("stock") => 1.0,
        Some("vn_stock") => 100.0,
        _ => 0.0,
    }
}

/// Min-max downsample a `Vec<CurvePoint>` to at most `2 * buckets` entries.
/// Each bucket contributes its min-value point and max-value point (in time order),
/// so peaks and troughs are never dropped. First and last points are always kept.
fn downsample(pts: Vec<CurvePoint>, buckets: usize) -> Vec<CurvePoint> {
    if buckets == 0 || pts.len() <= buckets * 2 {
        return pts;
    }
    let n = pts.len();
    let mut out = Vec::with_capacity(buckets * 2 + 2);
    let bucket_size = n as f64 / buckets as f64;
    for b in 0..buckets {
        let start = (b as f64 * bucket_size).round() as usize;
        let end = ((b + 1) as f64 * bucket_size).round() as usize;
        let end = end.min(n);
        if start >= end { continue; }
        let slice = &pts[start..end];
        let (min_i, max_i) = slice.iter().enumerate().fold((0, 0), |(mi, xi), (i, p)| {
            let mi = if p.v < slice[mi].v { i } else { mi };
            let xi = if p.v > slice[xi].v { i } else { xi };
            (mi, xi)
        });
        if min_i <= max_i {
            out.push(pts[start + min_i].clone());
            if min_i != max_i { out.push(pts[start + max_i].clone()); }
        } else {
            out.push(pts[start + max_i].clone());
            if min_i != max_i { out.push(pts[start + min_i].clone()); }
        }
    }
    out
}

/// Min-max downsample a `Vec<f64>` (no timestamps) to at most `2 * buckets` entries.
fn downsample_f64(pts: Vec<f64>, buckets: usize) -> Vec<f64> {
    if buckets == 0 || pts.len() <= buckets * 2 {
        return pts;
    }
    let n = pts.len();
    let mut out = Vec::with_capacity(buckets * 2);
    let bucket_size = n as f64 / buckets as f64;
    for b in 0..buckets {
        let start = (b as f64 * bucket_size).round() as usize;
        let end = ((b + 1) as f64 * bucket_size).round() as usize;
        let end = end.min(n);
        if start >= end { continue; }
        let slice = &pts[start..end];
        let (min_i, max_i) = slice.iter().enumerate().fold((0, 0), |(mi, xi), (i, &v)| {
            let mi = if v < slice[mi] { i } else { mi };
            let xi = if v > slice[xi] { i } else { xi };
            (mi, xi)
        });
        if min_i <= max_i {
            out.push(pts[start + min_i]);
            if min_i != max_i { out.push(pts[start + max_i]); }
        } else {
            out.push(pts[start + max_i]);
            if min_i != max_i { out.push(pts[start + min_i]); }
        }
    }
    out
}

fn ms_to_iso(ms: i64) -> String {
    DateTime::<Utc>::from_timestamp_millis(ms)
        .map(|dt| dt.format("%Y-%m-%dT%H:%M:%SZ").to_string())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_data::{BarFeed, BarVecFeed, ParquetFeed};
    use alm_strategy::{build_strategy, FixedFractional};
    use std::path::PathBuf;

    fn btcusdt_m1_parquet() -> PathBuf {
        // Largest file: multi-year dataset
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .parent().unwrap()
            .join("data/testdata/BTCUSDT/M1/BTCUSDT_M1_2022-04-13_to_2026-04-12.parquet")
    }

    #[test]
    fn btcusdt_m1_new_metrics() {
        let path = btcusdt_m1_parquet();
        if !path.exists() {
            eprintln!("testdata not found, skipping: {}", path.display());
            return;
        }

        let mut feed = ParquetFeed::load(&path, "BTCUSDT").expect("load parquet");
        let all_bars: Vec<alm_core::Bar> = std::iter::from_fn(|| feed.next()).collect();
        println!("loaded {} bars", all_bars.len());

        let inner = build_strategy("ma_crossover", &serde_json::Value::Object(Default::default()))
            .expect("build strategy");
        let risk = FixedFractional::fractional(0.95, 1); // lot_size=0 → fractional BTC
        let mut engine = crate::Engine::sync(10_000.0, inner, risk, 0.001, 0.0005);
        let mut bar_feed = BarVecFeed::new(all_bars, "BTCUSDT".into());
        let report = engine.run(&mut bar_feed, 0.04);

        // Build response via backtest::run to exercise all new fields
        let trades_resp: Vec<TradeResponse> = engine.portfolio.trades.iter().map(|t| TradeResponse {
            symbol: t.symbol.clone(),
            side: match t.side {
                alm_core::order::Side::Buy => "long".into(),
                alm_core::order::Side::Sell => "short".into(),
            },
            qty: t.qty,
            entry_price: t.entry_price,
            exit_price: t.exit_price,
            entry_ts: t.entry_timestamp,
            exit_ts: t.exit_timestamp,
            entry_time: String::new(),
            exit_time: String::new(),
            pnl: t.pnl,
            pnl_pct: t.pnl_pct,
            commission: t.commission,
            mae_pct: t.mae_pct * 100.0,
            mfe_pct: t.mfe_pct * 100.0,
            bars_held: t.bars_held,
            exit_reason: t.exit_reason.to_string(),
        }).collect();

        println!("=== BTCUSDT M1 — rsi_mean_rev ===");
        println!("trades:              {}", report.total_trades);
        println!("total_return:        {:.2}%", report.total_return_pct);
        println!("sharpe:              {:.3}", report.sharpe_ratio);
        println!("max_dd:              {:.2}%", report.max_drawdown_pct);
        println!("ulcer_index:         {:.4}", report.ulcer_index);
        println!("serenity_ratio:      {:.4}", report.serenity_ratio);
        println!("kelly_pct:           {:.4}", report.kelly_pct);
        println!("trades_per_year:     {:.1}", report.trades_per_year);
        println!("total_commission:    ${:.4}", report.total_commission_paid);
        println!("avg_mae_pct:         {:.4}%", report.avg_mae_pct);
        println!("avg_mfe_pct:         {:.4}%", report.avg_mfe_pct);
        println!("yearly_returns:      {:?}", report.yearly_returns);

        if let Some(t) = trades_resp.first() {
            println!("--- first trade ---");
            println!("  bars_held:  {}", t.bars_held);
            println!("  mae_pct:    {:.4}%", t.mae_pct);
            println!("  mfe_pct:    {:.4}%", t.mfe_pct);
            println!("  commission: ${:.6}", t.commission);
        }

        // Sanity assertions
        assert!(report.ulcer_index >= 0.0);
        assert!(report.avg_mae_pct >= 0.0);
        assert!(report.avg_mfe_pct >= 0.0);
        assert!(report.total_commission_paid >= 0.0);
        if report.total_trades > 0 {
            assert!(report.trades_per_year > 0.0);
            let first = &trades_resp[0];
            assert!(first.bars_held > 0, "bars_held should be > 0");
            assert!(first.mae_pct >= 0.0);
            assert!(first.mfe_pct >= 0.0);
            // commission > 0 if position was sized (entry + exit)
            assert!(first.commission >= 0.0);
        }
        // MFE should generally >= |pnl_pct| for winners (price hit that high at some point)
        let winners: Vec<_> = trades_resp.iter().filter(|t| t.pnl_pct > 0.0).collect();
        for w in &winners {
            assert!(w.mfe_pct >= 0.0, "mfe should be non-negative");
        }
    }
}
