//! Assemble `BacktestResponse` from engine output + report.

use std::collections::HashMap;

use alm_core::{Bar, order::Side};
use alm_report::{BuyHoldBenchmark, monte_carlo as mc_run, MonteCarloConfig as McConfig};
use chrono::{DateTime, Utc};

use crate::bus_sync::SyncBus;
use crate::curve_compress::compress;
use crate::types::{
    ActivityStats, BacktestRequest, BacktestResponse, BuyHoldBenchmarkResponse, CalendarStats,
    CapitalStats, CurvePoint, CurveStats, DistributionStats, DrawdownStats, ExcursionStats,
    ExitReasonBreakdownResponse, LongShortStats, MonteCarloResponse, RegimeSummaryResponse,
    RegimeTradeStatsResponse, ReturnStats, RiskAdjustedStats, TradeResponse, TradeStats,
    WalkForwardResponse,
};
use alm_report::BacktestReport;

use alm_core::strategy::Strategy;
use alm_strategy::AnySizer;
use super::engine_builder::StrengthFilter;
use crate::Engine;

pub fn build(
    mut engine: Engine<StrengthFilter, AnySizer, SyncBus>,
    report: BacktestReport,
    req: BacktestRequest,
    symbol: String,
    bar_count: usize,
    all_bars: &[Bar],
    capital: f64,
    risk_free: f64,
    curve_max: usize,
) -> BacktestResponse {
    let no_trades = report.total_trades == 0;
    let params = req.params.unwrap_or_default();

    // ── Benchmark ─────────────────────────────────────────────────────────────
    let closes: Vec<f64> = all_bars.iter().map(|b| b.close).collect();
    let timestamps: Vec<i64> = all_bars.iter().map(|b| b.timestamp).collect();
    let bh = BuyHoldBenchmark::compute(&closes, &timestamps, risk_free);
    let benchmark = (closes.len() >= 2).then(|| BuyHoldBenchmarkResponse {
        total_return_pct: bh.total_return_pct,
        cagr_pct: bh.cagr_pct,
        annualized_volatility_pct: bh.annualized_volatility_pct,
        sharpe_ratio: bh.sharpe_ratio,
        sortino_ratio: bh.sortino_ratio,
        max_drawdown_pct: bh.max_drawdown_pct,
        max_dd_duration_bars: bh.max_dd_duration_bars,
    });

    // ── Indicator series ──────────────────────────────────────────────────────
    let indicator_series: HashMap<String, Vec<CurvePoint>> = engine
        .strategy
        .take_indicator_series()
        .into_iter()
        .map(|(k, pts)| (k, pts.into_iter().map(|(t, v)| CurvePoint { t, v }).collect()))
        .collect();

    // ── Trades ────────────────────────────────────────────────────────────────
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

    // ── Equity / drawdown curves ──────────────────────────────────────────────
    let equity_curve: Vec<CurvePoint> = engine
        .portfolio
        .equity_curve
        .iter()
        .map(|p| CurvePoint { t: p.timestamp, v: p.equity })
        .collect();

    let drawdown_curve: Vec<CurvePoint> = {
        let mut peak = capital;
        engine
            .portfolio
            .equity_curve
            .iter()
            .map(|p| {
                if p.equity > peak { peak = p.equity; }
                let dd = if peak > 0.0 { (p.equity - peak) / peak } else { 0.0 };
                CurvePoint { t: p.timestamp, v: dd }
            })
            .collect()
    };

    // ── Exposure ──────────────────────────────────────────────────────────────
    let exposure_pct = {
        let total_ms = engine.portfolio.equity_curve
            .last()
            .and_then(|last| engine.portfolio.equity_curve.first().map(|f| last.timestamp - f.timestamp))
            .unwrap_or(0);
        let held_ms: i64 = engine.portfolio.trades.iter()
            .map(|t| t.exit_timestamp - t.entry_timestamp)
            .sum();
        if total_ms > 0 { held_ms as f64 / total_ms as f64 * 100.0 } else { 0.0 }
    };

    // ── Regime ────────────────────────────────────────────────────────────────
    let regime_summary = report.regime_summary.map(|r| RegimeSummaryResponse {
        changes: r.changes,
        trade_breakdown: r.trade_breakdown.into_iter().map(|s| RegimeTradeStatsResponse {
            label:          s.label,
            trades:         s.trades,
            win_rate_pct:   s.win_rate_pct,
            avg_return_pct: s.avg_return_pct,
            profit_factor:  s.profit_factor,
        }).collect(),
    });

    // ── Monte Carlo (opt-in) ──────────────────────────────────────────────────
    let monte_carlo = req.monte_carlo.and_then(|mc_cfg| {
        let pnl_pct: Vec<f64> = engine.portfolio.trades.iter().filter_map(|t| {
            let idx = engine.portfolio.equity_curve
                .partition_point(|p| p.timestamp <= t.entry_timestamp);
            let eq = if idx > 0 { engine.portfolio.equity_curve[idx - 1].equity } else { return None };
            if eq > f64::EPSILON { Some(t.pnl / eq) } else { None }
        }).collect();
        let cfg = McConfig {
            n_iter: mc_cfg.n_iter.unwrap_or(1_000),
            ruin_threshold: mc_cfg.ruin_threshold.unwrap_or(0.50),
            seed: mc_cfg.seed,
        };
        mc_run(&pnl_pct, capital, &cfg).map(|r| MonteCarloResponse {
            n_iter: r.n_iter,
            n_trades: r.n_trades,
            initial_capital: r.initial_capital,
            ruin_threshold: r.ruin_threshold,
            ruin_probability: r.ruin_probability,
            final_p5: r.final_p5, final_p10: r.final_p10, final_p25: r.final_p25,
            final_p50: r.final_p50, final_p75: r.final_p75, final_p90: r.final_p90,
            final_p95: r.final_p95,
            curve_p5: r.curve_p5, curve_p10: r.curve_p10, curve_p25: r.curve_p25,
            curve_p50: r.curve_p50, curve_p75: r.curve_p75, curve_p90: r.curve_p90,
            curve_p95: r.curve_p95,
        })
    });

    // ── Walk-forward — FUTURE[wf-v2] ─────────────────────────────────────────
    if req.walk_forward.is_some() {
        tracing::warn!("walk_forward field present but walk-forward is disabled — FUTURE[wf-v2]");
    }
    let walk_forward_result: Option<WalkForwardResponse> = None;

    BacktestResponse {
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
            sharpe:          if no_trades { 0.0 } else { report.sharpe_ratio },
            sortino:         if no_trades { 0.0 } else { report.sortino_ratio },
            calmar:          if no_trades { 0.0 } else { report.calmar_ratio },
            serenity:        if no_trades { 0.0 } else { report.serenity_ratio },
            omega:           if no_trades { 0.0 } else { report.omega_ratio },
            tail_ratio:      if no_trades { 0.0 } else { report.tail_ratio },
            recovery_factor: if no_trades { 0.0 } else { report.recovery_factor },
            var_95:          if no_trades { 0.0 } else { report.var_95 },
            cvar_95:         if no_trades { 0.0 } else { report.cvar_95 },
        },
        drawdown: DrawdownStats {
            max_pct:            report.max_drawdown_pct,
            max_duration_bars:  report.max_dd_duration_bars,
            avg_pct:            report.avg_drawdown_pct,
            ulcer_index:        if no_trades { 0.0 } else { report.ulcer_index },
        },
        trade_stats: TradeStats {
            total:                    report.total_trades,
            win_rate_pct:             report.win_rate_pct,
            profit_factor:            report.profit_factor,
            payoff_ratio:             report.payoff_ratio,
            expectancy:               report.expectancy,
            breakeven_win_rate_pct:   report.breakeven_win_rate_pct,
            gross_profit_usd:         report.gross_profit_usd,
            gross_loss_usd:           report.gross_loss_usd,
            avg_win_pct:              report.avg_win_pct,
            avg_loss_pct:             report.avg_loss_pct,
            avg_duration_hours:       report.avg_trade_duration_hours,
            avg_bars_held_winners:    report.avg_bars_held_winners,
            avg_bars_held_losers:     report.avg_bars_held_losers,
            max_consecutive_losses:   report.max_consecutive_losses,
            max_consecutive_wins:     report.max_consecutive_wins,
            largest_win_pct:          report.largest_win_pct,
            largest_loss_pct:         report.largest_loss_pct,
            mfe_capture_ratio:        report.mfe_capture_ratio,
            exit_reasons: ExitReasonBreakdownResponse {
                signal:         report.exit_reasons.signal,
                stop_loss:      report.exit_reasons.stop_loss,
                take_profit:    report.exit_reasons.take_profit,
                trailing_stop:  report.exit_reasons.trailing_stop,
                atr_stop:       report.exit_reasons.atr_stop,
                atr_target:     report.exit_reasons.atr_target,
                max_bars:       report.exit_reasons.max_bars,
                end_of_data:    report.exit_reasons.end_of_data,
            },
        },
        distribution: DistributionStats {
            skewness:         if no_trades { 0.0 } else { report.skewness },
            excess_kurtosis:  if no_trades { 0.0 } else { report.excess_kurtosis },
            sqn:              if no_trades { 0.0 } else { report.sqn },
            psr:              if no_trades { 0.0 } else { report.psr },
        },
        long_short: LongShortStats {
            long_trades:         report.long_stats.count,
            long_win_rate_pct:   report.long_stats.win_rate * 100.0,
            long_profit_factor:  report.long_stats.profit_factor,
            short_trades:        report.short_stats.count,
            short_win_rate_pct:  report.short_stats.win_rate * 100.0,
            short_profit_factor: report.short_stats.profit_factor,
        },
        excursion: ExcursionStats {
            avg_mae_pct:  report.avg_mae_pct,
            avg_mfe_pct:  report.avg_mfe_pct,
            mae_mfe_ratio: report.mae_mfe_ratio,
        },
        activity: ActivityStats {
            trades_per_year:      report.trades_per_year,
            exposure_pct,
            total_commission_usd: report.total_commission_paid,
            kelly_pct:            if no_trades { 0.0 } else { report.kelly_pct },
        },
        curves: CurveStats {
            equity:               compress(equity_curve, curve_max),
            drawdown:             compress(drawdown_curve, curve_max),
            rolling_sharpe:       downsample_f64(report.rolling_sharpe, curve_max),
            rolling_sharpe_std:   if no_trades { 0.0 } else { report.rolling_sharpe_std },
            rolling_drawdown:     downsample_f64(report.rolling_drawdown, curve_max),
        },
        calendar: CalendarStats {
            monthly_returns: report.monthly_returns.iter()
                .map(|&(y, m, r)| [y as f64, m as f64, r])
                .collect(),
            yearly_returns: report.yearly_returns.iter()
                .map(|&(y, r)| [y as f64, r])
                .collect(),
        },
        trades,
        indicator_series,
        regime_summary,
        benchmark,
        monte_carlo,
        walk_forward: walk_forward_result,
        walk_forward_note: req.walk_forward.as_ref().map(|_| "disabled — FUTURE[wf-v2]".into()),
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn ms_to_iso(ms: i64) -> String {
    DateTime::<Utc>::from_timestamp_millis(ms)
        .map(|dt| dt.format("%Y-%m-%dT%H:%M:%SZ").to_string())
        .unwrap_or_default()
}

/// Min-max downsample a `Vec<f64>` (no timestamps) to at most `2 * buckets` entries.
pub(super) fn downsample_f64(pts: Vec<f64>, buckets: usize) -> Vec<f64> {
    if buckets == 0 || pts.len() <= buckets * 2 {
        return pts;
    }
    let n = pts.len();
    let mut out = Vec::with_capacity(buckets * 2);
    let bucket_size = n as f64 / buckets as f64;
    for b in 0..buckets {
        let start = (b as f64 * bucket_size).round() as usize;
        let end = (((b + 1) as f64 * bucket_size).round() as usize).min(n);
        if start >= end { continue; }
        let slice = &pts[start..end];
        let (min_i, max_i) = slice.iter().enumerate().fold((0, 0), |(mi, xi), (i, &v)| {
            (if v < slice[mi] { i } else { mi }, if v > slice[xi] { i } else { xi })
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
