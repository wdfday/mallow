//! Backtest runner — executes a named / Rhai strategy over historical bars.
//!
//! # Modules
//! - [`loader`]         — Parquet file discovery + bar hydration
//! - [`engine_builder`] — `Engine` construction from a `BacktestRequest`
//! - [`response`]       — assembles `BacktestResponse` from engine output

pub mod engine_builder;
pub mod loader;
pub mod response;

pub use engine_builder::{asset_lot_size, intra_bar_mode_from_str};

use std::path::Path;

use alm_data::{BarFeed, BarVecFeed};
use anyhow::Result;
use serde_json::Value;

use crate::data::parse_date_ms;
use crate::types::{BacktestRequest, BacktestResponse};

use crate::data::{find_parquet_files, load_bars, market_region_from_data_source};

pub(crate) const DEFAULT_RISK_FREE: f64 = 0.04;
pub(crate) const DEFAULT_COMMISSION: f64 = 0.001;
pub(crate) const DEFAULT_SLIPPAGE: f64 = 0.0005;
pub(crate) const DEFAULT_CAPITAL: f64 = 10_000.0;
pub(crate) const DEFAULT_POSITION_PCT: f64 = 0.95;
pub(crate) const DEFAULT_CURVE_POINTS: usize = 2_000;

const MAX_BARS: usize = 100_000;

// ── Public entry points ───────────────────────────────────────────────────────

/// Estimate bar count for a backtest request without running the engine.
/// Returns `(bar_count, estimated_seconds)`.
pub fn estimate(req: &BacktestRequest, data_dir: &Path) -> Result<(usize, f64)> {
    let symbol = loader::effective_symbol(&req.symbol);
    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req.to.as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));
    let market_region = req.data_source.as_deref()
        .map(market_region_from_data_source)
        .unwrap_or("us");
    let market_hours_only = !market_region.is_empty();
    let files = find_parquet_files(data_dir, symbol, req.timeframe.as_deref(), req.data_source.as_deref());
    let feed = load_bars(&files, symbol, from_ms, to_ms, market_hours_only, market_region)
        .map_err(|e| anyhow::anyhow!("loading data for '{}': {}", symbol, e))?;
    let bar_count = feed.len();
    let bars_per_sec = match req.strategy.as_str() {
        "rhai" => 200_000.0,
        _ => 500_000.0,
    };
    Ok((bar_count, bar_count as f64 / bars_per_sec))
}

/// Run a full backtest from a request, discovering data under `data_dir`.
pub fn run(req: BacktestRequest, data_dir: &Path) -> Result<BacktestResponse> {
    let symbol = loader::effective_symbol(&req.symbol).to_string();
    let capital = req.initial_capital.unwrap_or(DEFAULT_CAPITAL);
    let risk_free = req.risk_free_annual.unwrap_or(DEFAULT_RISK_FREE);
    let curve_max = match req.curve_points {
        Some(0) => usize::MAX,
        Some(n) => n,
        None => DEFAULT_CURVE_POINTS,
    };
    let params = req.params.clone().unwrap_or(Value::Object(Default::default()));

    let all_bars = loader::load_bars_for_request(&req, data_dir)?;
    let bar_count = all_bars.len();

    if bar_count > MAX_BARS {
        let tf = req.timeframe.as_deref().unwrap_or("M1").to_uppercase();
        let suggestion = match tf.as_str() {
            "M1"  => "Use M5 or M15 for ranges > 3 months",
            "M5"  => "Use M15 or H1 for ranges > 1 year",
            "M15" => "Use H1 for ranges > 2 years",
            "H1"  => "Use H4 or D1 for ranges > 5 years",
            _     => "Narrow the date range",
        };
        anyhow::bail!("{} bars exceeds the {} bar limit. {}.", bar_count, MAX_BARS, suggestion);
    }

    tracing::info!(symbol = %symbol, strategy = %req.strategy, bars = bar_count, "starting backtest");

    let mut engine = engine_builder::build(&req, &params)?;
    let mut bar_feed = BarVecFeed::new(all_bars.clone(), symbol.clone());
    let report = engine.run(&mut bar_feed, risk_free);

    Ok(response::build(engine, report, req, symbol, bar_count, &all_bars, capital, risk_free, curve_max))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_data::{BarFeed, BarVecFeed, ParquetFeed};
    use alm_strategy::{build_strategy, FixedFractional};
    use crate::types::TradeResponse;
    use alm_core::order::Side;
    use std::path::PathBuf;

    fn btcusdt_m1_parquet() -> PathBuf {
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
        let risk = FixedFractional::fractional(0.95, 1);
        let mut engine = crate::Engine::sync(10_000.0, inner, risk, 0.001, 0.0005);
        let mut bar_feed = BarVecFeed::new(all_bars, "BTCUSDT".into());
        let report = engine.run(&mut bar_feed, 0.04);

        let trades_resp: Vec<TradeResponse> = engine.portfolio.trades.iter().map(|t| TradeResponse {
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
