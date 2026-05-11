//! Bar loading for backtest requests — file discovery + feed hydration.

use std::path::Path;

use alm_core::Bar;
use alm_data::BarFeed;
use anyhow::{Context, Result};

use crate::data::{find_parquet_files, load_bars, market_region_from_data_source, parse_date_ms};
use crate::types::BacktestRequest;

/// Returns `"BTCUSD"` when the symbol field is empty.
pub fn effective_symbol(s: &str) -> &str {
    if s.is_empty() { "BTCUSD" } else { s }
}

/// Discover Parquet files, load bars, and drain the feed into a `Vec<Bar>`.
pub fn load_bars_for_request(req: &BacktestRequest, data_dir: &Path) -> Result<Vec<Bar>> {
    let symbol = effective_symbol(&req.symbol);
    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    let to_ms = req
        .to
        .as_deref()
        .and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));
    let market_region = req
        .data_source
        .as_deref()
        .map(market_region_from_data_source)
        .unwrap_or("us");
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
