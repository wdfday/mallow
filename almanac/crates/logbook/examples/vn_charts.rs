//! VN30 backtest → HTML charts demo.
//!
//! Loads VCI daily parquet for one or more symbols, runs MA-Crossover + RSI-MeanRev
//! strategies, then writes ECharts HTML files:
//!   - {symbol}_candle.html  — candlestick + SMA20 + SMA50 + RSI + trade markers
//!   - {symbol}_equity.html  — equity curve + drawdown
//!
//! Usage:
//!   cargo run --example vn_charts
//!   DATA_DIR=/path/to/hist-data/data/VCI/vn SYMBOLS=VCB,FPT,VIC cargo run --example vn_charts

use std::env;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result};
use alm_chart::{candle::CandleChart, equity::EquityChart};
use alm_core::Bar;
use alm_data::{BarFeed, InMemoryFeed, ParquetFeed};
use alm_engine::Engine;
use alm_indicator::{Ema, Rsi, Sma};
use alm_strategy::{build_strategy, FixedFractional};
use serde_json::json;
use walkdir::WalkDir;

fn main() -> Result<()> {
    // ── Config from env ───────────────────────────────────────────────────────
    let data_dir = PathBuf::from(
        env::var("DATA_DIR").unwrap_or_else(|_| {
            // Default: relative to workspace root
            let manifest = env!("CARGO_MANIFEST_DIR");
            format!("{manifest}/../../../../hist-data/data/VCI/vn")
        }),
    );

    let symbols_env = env::var("SYMBOLS").unwrap_or_else(|_| "VCB,FPT,VIC,VNM".to_string());
    let symbols: Vec<&str> = symbols_env.split(',').map(str::trim).collect();

    let out_dir = PathBuf::from(
        env::var("OUT_DIR").unwrap_or_else(|_| "/tmp/vn_charts".to_string()),
    );
    std::fs::create_dir_all(&out_dir)?;

    println!("Data dir : {}", data_dir.display());
    println!("Symbols  : {:?}", symbols);
    println!("Output   : {}", out_dir.display());
    println!();

    // ── Run per symbol ────────────────────────────────────────────────────────
    for &symbol in &symbols {
        println!("── {symbol} ────────────────────────────────────");
        match run_symbol(symbol, &data_dir, &out_dir) {
            Ok(()) => println!("  ✓ charts written"),
            Err(e) => eprintln!("  ✗ {symbol}: {e:#}"),
        }
    }

    println!("\nDone. Open files in {}", out_dir.display());
    Ok(())
}

fn run_symbol(symbol: &str, data_dir: &Path, out_dir: &Path) -> Result<()> {
    // ── Load bars ─────────────────────────────────────────────────────────────
    let files = find_parquet(data_dir, symbol);
    if files.is_empty() {
        anyhow::bail!("no parquet files found for '{symbol}' under {}", data_dir.display());
    }

    let paths: Vec<&Path> = files.iter().map(PathBuf::as_path).collect();
    let mut feed = ParquetFeed::load_many(&paths, symbol)
        .with_context(|| format!("loading parquet for {symbol}"))?;

    let mut bars: Vec<Bar> = Vec::new();
    while let Some(b) = feed.next() {
        bars.push(b);
    }
    println!("  bars = {}", bars.len());

    if bars.len() < 60 {
        anyhow::bail!("too few bars ({}) for strategy warmup", bars.len());
    }

    // ── Compute indicator series (for chart overlay) ───────────────────────────
    let sma20 = compute_sma(&bars, 20);
    let sma50 = compute_sma(&bars, 50);
    let ema12 = compute_ema(&bars, 12);
    let rsi14 = compute_rsi(&bars, 14);

    // ── Run MA-Crossover strategy ─────────────────────────────────────────────
    let strategy_ma = build_strategy(
        "ma_crossover",
        &json!({ "fast_period": 20, "slow_period": 50, "signal_ema": false }),
    )?;
    let risk = FixedFractional::new(0.95, 1);

    let mut feed_ma = InMemoryFeed::new(bars.clone(), symbol.to_string());
    let mut engine_ma = Engine::sync(100_000.0, strategy_ma, risk, 0.001, 0.0005);
    let report_ma = engine_ma.run(&mut feed_ma, 0.04);

    println!(
        "  MA Crossover → return={:.1}% sharpe={:.2} trades={}",
        report_ma.total_return_pct,
        report_ma.sharpe_ratio,
        report_ma.total_trades,
    );

    // ── Run RSI-MeanRev strategy ───────────────────────────────────────────────
    let strategy_rsi = build_strategy(
        "rsi_mean_rev",
        &json!({ "rsi_period": 14, "oversold": 30.0, "overbought": 70.0 }),
    )?;
    let risk2 = FixedFractional::new(0.95, 1);

    let mut feed_rsi = InMemoryFeed::new(bars.clone(), symbol.to_string());
    let mut engine_rsi = Engine::sync(100_000.0, strategy_rsi, risk2, 0.001, 0.0005);
    let report_rsi = engine_rsi.run(&mut feed_rsi, 0.04);

    println!(
        "  RSI MeanRev  → return={:.1}% sharpe={:.2} trades={}",
        report_rsi.total_return_pct,
        report_rsi.sharpe_ratio,
        report_rsi.total_trades,
    );

    // ── Candlestick chart — MA Crossover ──────────────────────────────────────
    let trades_ma = &engine_ma.portfolio.trades;
    let candle_html = CandleChart::new(&bars)
        .with_title(format!("{symbol} — MA Crossover (SMA20/50)"))
        .indicator("SMA 20", sma20)
        .indicator("SMA 50", sma50)
        .indicator("EMA 12", ema12)
        .volume(true)
        .trades(trades_ma)
        .to_html()
        .context("candle html")?;

    let candle_path = out_dir.join(format!("{symbol}_ma_candle.html"));
    std::fs::write(&candle_path, candle_html)?;

    // ── Candlestick chart — RSI MeanRev ──────────────────────────────────────
    let trades_rsi = &engine_rsi.portfolio.trades;
    let candle_rsi_html = CandleChart::new(&bars)
        .with_title(format!("{symbol} — RSI MeanRev (14)"))
        .indicator("RSI 14", rsi14)
        .volume(true)
        .trades(trades_rsi)
        .to_html()
        .context("candle rsi html")?;

    let candle_rsi_path = out_dir.join(format!("{symbol}_rsi_candle.html"));
    std::fs::write(&candle_rsi_path, candle_rsi_html)?;

    // ── Equity chart — both strategies ────────────────────────────────────────
    let equity_ma_html = EquityChart::new(&engine_ma.portfolio)
        .with_title(format!("{symbol} Equity — MA Crossover"))
        .to_html()
        .context("equity ma html")?;
    std::fs::write(out_dir.join(format!("{symbol}_ma_equity.html")), equity_ma_html)?;

    let equity_rsi_html = EquityChart::new(&engine_rsi.portfolio)
        .with_title(format!("{symbol} Equity — RSI MeanRev"))
        .to_html()
        .context("equity rsi html")?;
    std::fs::write(out_dir.join(format!("{symbol}_rsi_equity.html")), equity_rsi_html)?;

    println!(
        "  Files: {s}_ma_candle.html  {s}_rsi_candle.html  {s}_ma_equity.html  {s}_rsi_equity.html",
        s = symbol
    );

    Ok(())
}

// ── Indicator helpers ─────────────────────────────────────────────────────────

fn compute_sma(bars: &[Bar], period: usize) -> Vec<Option<f64>> {
    let mut sma = Sma::new(period);
    bars.iter().map(|b| sma.update(b.close)).collect()
}

fn compute_ema(bars: &[Bar], period: usize) -> Vec<Option<f64>> {
    let mut ema = Ema::new(period);
    bars.iter().map(|b| ema.update(b.close)).collect()
}

fn compute_rsi(bars: &[Bar], period: usize) -> Vec<Option<f64>> {
    let mut rsi = Rsi::new(period);
    bars.iter().map(|b| rsi.update(b.close)).collect()
}

// ── File discovery ────────────────────────────────────────────────────────────

fn find_parquet(data_dir: &Path, symbol: &str) -> Vec<PathBuf> {
    let sym_lower = symbol.to_lowercase();
    let mut files: Vec<PathBuf> = WalkDir::new(data_dir)
        .follow_links(false)
        .into_iter()
        .filter_map(|e| e.ok())
        .filter(|e| {
            if !e.file_type().is_file() {
                return false;
            }
            let path = e.path();
            if path.extension().and_then(|s| s.to_str()) != Some("parquet") {
                return false;
            }
            path.parent()
                .and_then(|p| p.file_name())
                .and_then(|n| n.to_str())
                .map(|n| n.to_lowercase() == sym_lower)
                .unwrap_or(false)
        })
        .map(|e| e.into_path())
        .collect();

    files.sort();
    files
}
