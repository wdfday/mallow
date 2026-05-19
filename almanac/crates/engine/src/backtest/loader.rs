//! Bar loading for backtest requests — file discovery + feed hydration.

use std::path::Path;

use alm_core::Bar;
use alm_data::BarFeed;
use anyhow::{Context, Result};

use crate::data::{find_parquet_files, load_bars, market_region_from_data_source, parse_date_ms};
use crate::types::BacktestRequest;

/// Returns `"BTCUSD"` when the symbol field is empty.
///
/// Accepts both raw (`"BTCUSDT"`) and exchange-prefixed (`"binance:BTCUSDT"`)
/// forms — herald's live endpoints use prefixed keys for ledger routing, but
/// parquet files on disk are organised by raw ticker. The prefix is stripped
/// here so backtest loading works regardless of which form the caller sends.
pub fn effective_symbol(s: &str) -> &str {
    let s = if s.is_empty() { "BTCUSD" } else { s };
    match s.find(':') {
        Some(pos) => &s[pos + 1..],
        None      => s,
    }
}

/// Low-level bar loader: load a specific symbol + TF combination.
///
/// Used by MTF backtest to load each timeframe's feed independently.
pub fn load_bars_for_tf(
    symbol: &str,
    tf: Option<&str>,
    data_source: Option<&str>,
    from_ms: Option<i64>,
    to_ms: Option<i64>,
    data_dir: &std::path::Path,
) -> anyhow::Result<Vec<alm_core::Bar>> {
    let market_region = data_source
        .map(market_region_from_data_source)
        .unwrap_or("");
    let market_hours_only = !market_region.is_empty();
    let files = find_parquet_files(data_dir, symbol, tf, data_source);
    let mut feed = load_bars(&files, symbol, from_ms, to_ms, market_hours_only, market_region)
        .with_context(|| {
            format!(
                "loading bars for '{}' tf={}",
                symbol,
                tf.unwrap_or("auto")
            )
        })?;
    Ok(std::iter::from_fn(|| feed.next()).collect())
}

/// Discover Parquet files, load bars, and drain the feed into a `Vec<Bar>`.
pub fn load_bars_for_request(req: &BacktestRequest, data_dir: &Path) -> Result<Vec<Bar>> {
    let symbol = effective_symbol(&req.symbol);
    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req
        .to
        .as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));
    // Default to 24/7 (no filter) when data_source is omitted; the previous
    // "us" default silently dropped weekend / off-hours bars for crypto.
    let market_region = req
        .data_source
        .as_deref()
        .map(market_region_from_data_source)
        .unwrap_or("");
    let market_hours_only = !market_region.is_empty();
    let files = find_parquet_files(
        data_dir,
        symbol,
        req.timeframe.as_deref(),
        req.data_source.as_deref(),
    );
    let mut feed = load_bars(&files, symbol, from_ms, to_ms, market_hours_only, market_region)
        .with_context(|| format!("loading data for '{}'", symbol))?;
    Ok(std::iter::from_fn(|| feed.next()).collect())
}
