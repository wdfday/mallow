use anyhow::Result;
use alm_data::{BarFeed, ParquetFeed};
use alm_engine::Engine;
use alm_strategy::{FixedFractional, MaCrossover};
use std::path::{Path, PathBuf};
use tracing::info;

fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .init();

    // --- Single backtest example ---
    let data_dir = PathBuf::from(
        std::env::var("DATA_DIR").unwrap_or_else(|_| "../../us-data/data/Polygon/stocks".into()),
    );

    let symbol = std::env::var("SYMBOL").unwrap_or_else(|_| "AAPL".into());
    let parquet_path = find_parquet(&data_dir, &symbol)?;

    info!(%symbol, path = %parquet_path.display(), "loading data");

    let mut feed = ParquetFeed::load(&parquet_path, &symbol)?;
    info!(bars = feed.len(), "data loaded");

    let strategy = MaCrossover::new(20, 50);
    let risk = FixedFractional::new(0.02, 5);

    let mut engine = Engine::sync(
        100_000.0, // initial capital $100K
        strategy, risk, 0.001,  // 0.1% commission
        0.0005, // 0.05% slippage
    );

    let t0 = std::time::Instant::now();
    let report = engine.run(&mut feed, 0.04); // 4% risk-free rate
    let elapsed = t0.elapsed();

    info!(
        strategy = %report.strategy,
        symbol = %report.symbol,
        total_return = format!("{:.2}%", report.total_return_pct),
        cagr = format!("{:.2}%", report.cagr_pct),
        sharpe = format!("{:.3}", report.sharpe_ratio),
        max_dd = format!("{:.2}%", report.max_drawdown_pct),
        trades = report.total_trades,
        win_rate = format!("{:.1}%", report.win_rate_pct),
        elapsed_ms = elapsed.as_millis(),
        "backtest complete"
    );

    println!("{}", serde_json::to_string_pretty(&report)?);

    Ok(())
}

fn find_parquet(data_dir: &Path, symbol: &str) -> Result<PathBuf> {
    let symbol_dir = data_dir.join(symbol);
    let entry = std::fs::read_dir(&symbol_dir)
        .map_err(|e| anyhow::anyhow!("cannot read {}: {}", symbol_dir.display(), e))?
        .filter_map(|e| e.ok())
        .find(|e| e.path().extension().and_then(|ext| ext.to_str()) == Some("parquet"))
        .ok_or_else(|| anyhow::anyhow!("no parquet file for {symbol}"))?;

    Ok(entry.path())
}
