//! Backtest runner — executes a named / script strategy over historical bars.
//!
//! # Modules
//! - [`loader`]         — Parquet file discovery + bar hydration
//! - [`engine_builder`] — `Engine` construction from a `BacktestRequest`
//! - [`response`]       — assembles `BacktestResponse` from engine output

pub mod engine_builder;
pub mod loader;
pub mod response;
pub mod mtf_response;

pub use engine_builder::{asset_lot_size, intra_bar_mode_from_str};

use std::path::Path;

use alm_core::{Bar, MtfStrategy, Timeframe};
use alm_data::{BarFeed, BarVecFeed};
use alm_strategy::{
    build_mtf_strategy, probe_script_htfs,
    MtfScriptStrategy, ScriptStrategy,
};
use anyhow::{bail, Result};
use serde_json::Value;

use crate::pointer_sync_mtf_engine::PointerSyncMtfEngine;
use crate::types::{BacktestRequest, BacktestResponse, CurvePoint, MtfBacktestRequest};

use crate::data::{find_parquet_files, load_bars, market_region_from_data_source, parse_date_ms};
use crate::backtest::loader::load_bars_for_tf;

pub(crate) const DEFAULT_RISK_FREE: f64 = 0.04;
pub(crate) const DEFAULT_COMMISSION: f64 = 0.001;
pub(crate) const DEFAULT_SLIPPAGE: f64 = 0.0005;
pub(crate) const DEFAULT_CAPITAL: f64 = 10_000.0;
pub(crate) const DEFAULT_POSITION_PCT: f64 = 0.95;
/// Max LTTB target — chart widths rarely exceed ~2k px, so more points
/// would only waste bandwidth without visible benefit.
pub(crate) const CURVE_TARGET_MAX: usize = 2_000;

/// Floor for LTTB target — even tiny backtests get a reasonable curve.
pub(crate) const CURVE_TARGET_FLOOR: usize = 400;

const MAX_BARS: usize = 100_000;

/// Auto-derive a curve compression target from input size.
/// - Small backtests (< floor bars): keep everything; compression is a no-op.
/// - Mid-range: linear in `bar_count` so detail scales with data size.
/// - Large (> max bars): cap at `CURVE_TARGET_MAX`.
///
/// The downstream `compress` pipeline also runs `dedup_flat` first, so
/// idle periods are stripped regardless of this target.
pub(crate) fn estimate_curve_target(bar_count: usize) -> usize {
    bar_count
        .max(CURVE_TARGET_FLOOR)
        .min(CURVE_TARGET_MAX)
}

// ── Public entry points ───────────────────────────────────────────────────────

/// Estimate bar count for a backtest request without running the engine.
/// Returns `(bar_count, estimated_seconds)`.
pub fn estimate(req: &BacktestRequest, data_dir: &Path) -> Result<(usize, f64)> {
    let symbol = loader::effective_symbol(&req.symbol);
    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req.to.as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));
    // Default to "" (24/7, no filter) when caller doesn't specify a source —
    // crypto is the most common case and was previously being wrongly clamped
    // to US market hours, dropping ~75% of bars.
    let market_region = req.data_source.as_deref()
        .map(market_region_from_data_source)
        .unwrap_or("");
    let market_hours_only = !market_region.is_empty();
    let files = find_parquet_files(data_dir, symbol, req.timeframe.as_deref(), req.data_source.as_deref());
    let feed = load_bars(&files, symbol, from_ms, to_ms, market_hours_only, market_region)
        .map_err(|e| anyhow::anyhow!("loading data for '{}': {}", symbol, e))?;
    let bar_count = feed.len();
    let bars_per_sec = match req.strategy.as_str() {
        "script" => 200_000.0,
        _ => 500_000.0,
    };
    Ok((bar_count, bar_count as f64 / bars_per_sec))
}

/// Run a full backtest from a request, discovering data under `data_dir`.
///
/// When `strategy == "script"` and the script declares HTF indicators
/// (e.g. `ind.ema(20, "H1")`), the runner auto-detects this and routes to
/// [`run_mtf_script`] — using `MtfScriptStrategy` + `MtfEngine` with real HTF
/// parquet feeds instead of the v1 internal resampler.
pub fn run(req: BacktestRequest, data_dir: &Path) -> Result<BacktestResponse> {
    // ── Auto-detect MTF script ────────────────────────────────────────────────
    //
    // Cheap parse-only probe — no Rhai AST compile, no IndicatorBox allocation.
    // V1 path will reject any TF-bearing script anyway, so probing first is the
    // only way to keep MTF scripts working through the unified `run()` entry.
    if req.strategy == "script" {
        let params = req.params.clone().unwrap_or_default();
        if let Some(script) = params.get("script").and_then(|v| v.as_str()) {
            let htfs = probe_script_htfs(script);
            if !htfs.is_empty() {
                let base_tf_str = req.timeframe.as_deref().unwrap_or("M1");
                let base_tf = parse_timeframe(base_tf_str)?;
                let strategy = MtfScriptStrategy::from_script(script, base_tf)?;
                return run_mtf_script(req, strategy, htfs, data_dir);
            }
        }
    }

    let symbol = loader::effective_symbol(&req.symbol).to_string();
    let capital = req.initial_capital.unwrap_or(DEFAULT_CAPITAL);
    let risk_free = req.risk_free_annual.unwrap_or(DEFAULT_RISK_FREE);
    let params = req.params.clone().unwrap_or(Value::Object(Default::default()));

    // Warm indicators on extra history before `from` (≈ 2.5× period for EMA-family,
    // 1× for SMA/window) so they're converged at the trading start. `trading_from_ms`
    // is the original `from`; bars before it only warm the engine, don't trade.
    let warmup_bars = loader::compute_warmup_bars(&req);
    let (all_bars, trading_from_ms) = loader::load_bars_warmed(&req, data_dir, warmup_bars)?;

    // Report range / benchmark / bar_count cover only the trading window (≥ from).
    let bar_count = match trading_from_ms {
        Some(f) => all_bars.iter().filter(|b| b.timestamp >= f).count(),
        None => all_bars.len(),
    };
    let curve_max = estimate_curve_target(bar_count);

    if all_bars.len() > MAX_BARS {
        let tf = req.timeframe.as_deref().unwrap_or("M1").to_uppercase();
        let suggestion = match tf.as_str() {
            "M1"  => "Use M5 or M15 for ranges > 3 months",
            "M5"  => "Use M15 or H1 for ranges > 1 year",
            "M15" => "Use H1 for ranges > 2 years",
            "H1"  => "Use H4 or D1 for ranges > 5 years",
            _     => "Narrow the date range",
        };
        anyhow::bail!("{} bars exceeds the {} bar limit. {}.", all_bars.len(), MAX_BARS, suggestion);
    }

    tracing::info!(symbol = %symbol, strategy = %req.strategy, bars = bar_count, "starting backtest");

    // For script strategies, attach an error sink so the first Rhai runtime error
    // can be surfaced to the caller rather than being silently swallowed.
    let script_error_sink: Option<std::sync::Arc<std::sync::Mutex<Option<String>>>> =
        if req.strategy == "script" {
            Some(std::sync::Arc::new(std::sync::Mutex::new(None)))
        } else {
            None
        };

    let mut engine = if let Some(sink) = script_error_sink.clone() {
        let script_text = params.get("script").and_then(|v| v.as_str())
            .ok_or_else(|| anyhow::anyhow!("ScriptStrategy requires a 'script' param"))?;
        use alm_core::strategy::Strategy;
        let strategy: Box<dyn Strategy> = Box::new(
            ScriptStrategy::build(script_text, true)?
                .with_error_sink(sink),
        );
        engine_builder::build_with_strategy(&req, strategy)?
    } else {
        engine_builder::build(&req, &params)?
    };
    if let Some(f) = trading_from_ms {
        engine = engine.with_warmup_until(f);
    }
    let mut bar_feed = BarVecFeed::new(all_bars.clone(), symbol.clone());
    let report = engine.run(&mut bar_feed, risk_free);

    // Surface the first Rhai runtime error — gives the caller a 400 with a
    // meaningful message instead of silently returning 0 trades.
    if let Some(sink) = script_error_sink {
        if let Ok(guard) = sink.lock() {
            if let Some(err_msg) = guard.as_deref() {
                anyhow::bail!("script runtime error: {}", err_msg);
            }
        }
    }

    // Benchmark / curves over the trading window only (drop warm-up bars before `from`).
    let trading_bars: Vec<Bar> = match trading_from_ms {
        Some(f) => all_bars.into_iter().filter(|b| b.timestamp >= f).collect(),
        None => all_bars,
    };
    Ok(response::build(engine, report, req, symbol, bar_count, &trading_bars, capital, risk_free, curve_max))
}

/// Run a multi-timeframe backtest from a [`MtfBacktestRequest`].
///
/// Loads one independent bar feed per timeframe, builds a [`MtfEngine`] with
/// the named [`MtfStrategy`], and returns the same [`BacktestResponse`] shape
/// as the single-TF runner.
pub fn run_mtf(req: MtfBacktestRequest, data_dir: &Path) -> Result<BacktestResponse> {
    if req.htf_timeframes.is_empty() {
        bail!("`htf_timeframes` must contain at least one entry");
    }

    let symbol = loader::effective_symbol(&req.symbol).to_string();
    let capital = req.initial_capital.unwrap_or(DEFAULT_CAPITAL);
    let commission = req.commission_pct.unwrap_or(DEFAULT_COMMISSION);
    let slippage = req.slippage_pct.unwrap_or(DEFAULT_SLIPPAGE);
    let risk_free = req.risk_free_annual.unwrap_or(DEFAULT_RISK_FREE);
    let lot_size = asset_lot_size(req.asset_type.as_deref());
    let max_positions = req.max_positions.unwrap_or(1).max(1);
    let params = req.params.clone().unwrap_or(Value::Object(Default::default()));

    let base_tf_str = req.base_tf.as_deref().unwrap_or("M1");
    let base_tf = parse_timeframe(base_tf_str)?;

    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req
        .to
        .as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));

    // Named MTF has no script to scan per-indicator, so warm each feed by a fixed,
    // generous count in its OWN bars (covers e.g. ema(100)×2.5) — enough for HTF
    // indicators to converge before `from`. The engine trades from `from`.
    const MTF_NAMED_WARMUP: i64 = 250;
    let warm_from = |tf: Timeframe| -> Option<i64> {
        from_ms.map(|f| f - MTF_NAMED_WARMUP * tf.duration_ms())
    };

    // Load base TF bars (also used for buy-hold benchmark).
    let base_bars = load_bars_for_tf(
        &symbol,
        Some(base_tf_str),
        req.data_source.as_deref(),
        warm_from(base_tf),
        to_ms,
        data_dir,
    )?;
    if base_bars.is_empty() {
        bail!("no bars found for symbol '{}' at base timeframe '{}'", symbol, base_tf_str);
    }
    let base_bar_count = match from_ms {
        Some(f) => base_bars.iter().filter(|b| b.timestamp >= f).count(),
        None => base_bars.len(),
    };

    // Build risk manager.
    let risk = engine_builder::build_sizer(
        None, // MTF: legacy field inference (no explicit size_mode)
        req.position_size_quantity,
        req.position_size_usd,
        req.risk_per_trade_pct,
        req.atr_multiplier,
        req.position_size_pct,
        req.strength_sizing,
        max_positions,
        lot_size,
    );

    let strategy = build_mtf_strategy(&req.strategy, &params)?;
    let strategy_name = strategy.name().to_string();

    let curve_max = estimate_curve_target(base_bar_count);

    tracing::info!(
        symbol = %symbol,
        strategy = %strategy_name,
        base_tf = %base_tf_str,
        htf = ?req.htf_timeframes,
        base_bars = base_bar_count,
        "starting MTF backtest",
    );

    let mut engine = PointerSyncMtfEngine::sync(capital, strategy, risk, commission, slippage)
        .with_base_tf(base_tf)
        .with_single_entry()
        .with_warmup_until(from_ms.unwrap_or(0));

    engine.add_feed(base_tf, BarVecFeed::new(base_bars.clone(), symbol.clone()));

    for tf_str in &req.htf_timeframes {
        let htf = parse_timeframe(tf_str)?;
        let htf_bars = load_bars_for_tf(
            &symbol,
            Some(tf_str.as_str()),
            req.data_source.as_deref(),
            warm_from(htf),
            to_ms,
            data_dir,
        )?;
        if htf_bars.is_empty() {
            bail!(
                "no bars found for symbol '{}' at HTF '{}'",
                symbol,
                tf_str
            );
        }
        engine.add_feed(htf, BarVecFeed::new(htf_bars, symbol.clone()));
    }

    let report = engine.run(risk_free);

    let monte_carlo_cfg = req.monte_carlo;
    let trading_base: Vec<Bar> = match from_ms {
        Some(f) => base_bars.into_iter().filter(|b| b.timestamp >= f).collect(),
        None => base_bars,
    };
    Ok(mtf_response::build(
        engine,
        report,
        strategy_name,
        symbol,
        base_bar_count,
        &trading_base,
        capital,
        risk_free,
        curve_max,
        monte_carlo_cfg,
        std::collections::HashMap::new(),
    ))
}

/// Run a Rhai v2 MTF script backtest.
///
/// Called automatically from [`run`] when the script declares HTF indicators.
/// Feeds `MtfEngine` with real parquet files for each declared TF — no resampling.
fn run_mtf_script(
    req: BacktestRequest,
    strategy: MtfScriptStrategy,
    htfs: Vec<Timeframe>,
    data_dir: &Path,
) -> Result<BacktestResponse> {
    let symbol = loader::effective_symbol(&req.symbol).to_string();
    let capital = req.initial_capital.unwrap_or(DEFAULT_CAPITAL);
    let commission = req.commission_pct.unwrap_or(DEFAULT_COMMISSION);
    let slippage = req.slippage_pct.unwrap_or(DEFAULT_SLIPPAGE);
    let risk_free = req.risk_free_annual.unwrap_or(DEFAULT_RISK_FREE);
    let lot_size = asset_lot_size(req.asset_type.as_deref());
    let max_positions = req.max_positions.unwrap_or(1).max(1);

    let base_tf_str = req.timeframe.as_deref().unwrap_or("M1");
    let base_tf = parse_timeframe(base_tf_str)?;

    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req.to.as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));

    // Per-TF warm-up. HTF indicators need many base bars of history to converge, so
    // each feed is loaded with extra bars before `from` (period × factor of THAT TF).
    // `warm_from(tf)` = the shifted load-start for a feed; engine trades from `from`.
    let script_txt = req.params.as_ref()
        .and_then(|p| p.get("script")).and_then(|v| v.as_str()).unwrap_or("");
    let warm_per_tf = loader::script_warmup_per_tf(script_txt, base_tf);
    let warm_from = |tf: Timeframe| -> Option<i64> {
        from_ms.map(|f| f - warm_per_tf.get(&tf).copied().unwrap_or(0) as i64 * tf.duration_ms())
    };

    let base_bars = load_bars_for_tf(
        &symbol,
        Some(base_tf_str),
        req.data_source.as_deref(),
        warm_from(base_tf),
        to_ms,
        data_dir,
    )?;
    if base_bars.is_empty() {
        anyhow::bail!(
            "no bars found for '{}' at base timeframe '{}'",
            symbol, base_tf_str
        );
    }
    // Report range / benchmark / bar_count cover only the trading window (≥ from).
    let bar_count = match from_ms {
        Some(f) => base_bars.iter().filter(|b| b.timestamp >= f).count(),
        None => base_bars.len(),
    };

    if base_bars.len() > MAX_BARS {
        anyhow::bail!(
            "{} bars exceeds the {} bar limit. Narrow the date range or use a higher timeframe.",
            base_bars.len(), MAX_BARS
        );
    }

    let risk = engine_builder::build_sizer(
        None, // MTF: legacy field inference (no explicit size_mode)
        req.position_size_quantity,
        req.position_size_usd,
        req.risk_per_trade_pct,
        req.atr_multiplier,
        req.position_size_pct,
        req.strength_sizing,
        max_positions,
        lot_size,
    );

    let curve_max = estimate_curve_target(bar_count);
    let strategy_name = strategy.name().to_string();

    tracing::info!(
        symbol = %symbol,
        strategy = %strategy_name,
        base_tf = %base_tf_str,
        htf = ?htfs,
        bars = bar_count,
        "starting MTF script backtest (v2 auto-detected)",
    );

    let mut engine = PointerSyncMtfEngine::sync(capital, strategy, risk, commission, slippage)
        .with_base_tf(base_tf)
        .with_single_entry()
        .with_warmup_until(from_ms.unwrap_or(0));

    engine.add_feed(base_tf, BarVecFeed::new(base_bars.clone(), symbol.clone()));

    for htf in &htfs {
        let tf_str = htf.to_string();
        let htf_bars = load_bars_for_tf(
            &symbol,
            Some(tf_str.as_str()),
            req.data_source.as_deref(),
            warm_from(*htf),
            to_ms,
            data_dir,
        )?;
        if htf_bars.is_empty() {
            anyhow::bail!(
                "no bars found for '{}' at HTF '{}' — needed by script",
                symbol, tf_str
            );
        }
        engine.add_feed(*htf, BarVecFeed::new(htf_bars, symbol.clone()));
    }

    let report = engine.run(risk_free);

    // Drain plot series collected during the run.
    let raw_series = engine.strategy.take_series();
    let indicator_series = raw_series
        .into_iter()
        .map(|(k, pts)| (k, pts.into_iter().map(|(t, v)| CurvePoint { t, v }).collect()))
        .collect();

    // Benchmark over the trading window only (drop warm-up bars before `from`).
    let trading_base: Vec<Bar> = match from_ms {
        Some(f) => base_bars.into_iter().filter(|b| b.timestamp >= f).collect(),
        None => base_bars,
    };
    Ok(mtf_response::build(
        engine,
        report,
        strategy_name,
        symbol,
        bar_count,
        &trading_base,
        capital,
        risk_free,
        curve_max,
        req.monte_carlo,
        indicator_series,
    ))
}

/// Parse a timeframe string to [`Timeframe`], case-insensitive.
fn parse_timeframe(s: &str) -> Result<Timeframe> {
    match s.to_uppercase().as_str() {
        "M1"  => Ok(Timeframe::M1),
        "M3"  => Ok(Timeframe::M3),
        "M5"  => Ok(Timeframe::M5),
        "M10" => Ok(Timeframe::M10),
        "M15" => Ok(Timeframe::M15),
        "M30" => Ok(Timeframe::M30),
        "H1"  => Ok(Timeframe::H1),
        "H2"  => Ok(Timeframe::H2),
        "H4"  => Ok(Timeframe::H4),
        "H6"  => Ok(Timeframe::H6),
        "H12" => Ok(Timeframe::H12),
        "D1"  => Ok(Timeframe::D1),
        "W1"  => Ok(Timeframe::W1),
        other => bail!("unknown timeframe '{other}'. Expected one of: M1 M3 M5 M10 M15 M30 H1 H2 H4 H6 H12 D1 W1"),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_data::{BarFeed, BarVecFeed, ParquetFeed};
    use alm_strategy::build_strategy;
    use crate::FixedFractional;
    use crate::types::TradeResponse;
    use alm_core::order::Side;
    use std::path::PathBuf;

    fn load_btcusdt_m1_test_bars() -> Option<Vec<alm_core::Bar>> {
        // 1. Try to load from real mallow/data/BinanceFlat/M1/BTCUSDT first
        let real_data_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .parent().unwrap().parent().unwrap() // mallow/almanac/crates -> mallow/almanac
            .parent().unwrap() // mallow
            .join("data/BinanceFlat/M1/BTCUSDT");

        let mut files = Vec::new();
        if let Ok(entries) = std::fs::read_dir(&real_data_dir) {
            for entry in entries.filter_map(|e| e.ok()) {
                let path = entry.path();
                if path.extension().map_or(false, |ext| ext == "parquet") {
                    files.push(path);
                }
            }
        }
        files.sort();

        let mut feed = if !files.is_empty() {
            let refs: Vec<&std::path::Path> = files.iter().map(|p| p.as_path()).collect();
            ParquetFeed::load_many(&refs, "BTCUSDT").ok()?
        } else {
            // Fallback to testdata
            let testdata_path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .parent().unwrap()
                .join("data/testdata/BTCUSDT/M1/BTCUSDT_M1_2026-01.parquet");
            ParquetFeed::load(&testdata_path, "BTCUSDT").ok()?
        };

        Some(std::iter::from_fn(|| feed.next()).collect())
    }

    #[test]
    fn btcusdt_m1_new_metrics() {
        let Some(all_bars) = load_btcusdt_m1_test_bars() else {
            eprintln!("testdata not found, skipping");
            return;
        };
        println!("loaded {} bars", all_bars.len());

        let inner = build_strategy("ma_crossover", &serde_json::Value::Object(Default::default()))
            .expect("build strategy");
        let risk = FixedFractional::fractional(0.95, 1);
        let mut engine = crate::Engine::sync(10_000.0, inner, risk, 0.001, 0.0005);
        let mut bar_feed = BarVecFeed::new(all_bars, "BTCUSDT".into());
        let report = engine.run(&mut bar_feed, 0.04);

        let trades_resp: Vec<TradeResponse> = engine.core.portfolio.trades.iter().map(|t| TradeResponse {
            symbol: t.symbol.clone(),
            side: match t.side { Side::Buy => "long".into(), Side::Sell => "short".into() },
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

        println!("=== BTCUSDT M1 — ma_crossover ===");
        println!("trades:           {}", report.total_trades);
        println!("total_return:     {:.2}%", report.total_return_pct);
        println!("sharpe:           {:.3}", report.sharpe_ratio);
        println!("max_dd:           {:.2}%", report.max_drawdown_pct);

        assert!(report.ulcer_index >= 0.0);
        assert!(report.avg_mae_pct >= 0.0);
        assert!(report.avg_mfe_pct >= 0.0);
        assert!(report.total_commission_paid >= 0.0);
        if report.total_trades > 0 {
            assert!(report.trades_per_year > 0.0);
            let first = &trades_resp[0];
            assert!(first.bars_held > 0);
            assert!(first.mae_pct >= 0.0);
            assert!(first.mfe_pct >= 0.0);
            assert!(first.commission >= 0.0);
        }
        let winners: Vec<_> = trades_resp.iter().filter(|t| t.pnl_pct > 0.0).collect();
        for w in &winners {
            assert!(w.mfe_pct >= 0.0);
        }
    }
}
