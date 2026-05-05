//! DuckDB core query logic and parquet fallback helpers.
//!
//! These functions are called from:
//! - `data/unified.rs` (transparent parquet fallback)

use std::path::Path;

use tracing::debug;

use alm_core::Bar;
use alm_engine::data::find_parquet_files;

use super::types::BarRecord;

// ── Parquet fallback helpers ──────────────────────────────────────────────────

/// Query bars with `timestamp < before_ms` from Parquet files, newest `limit`
/// bars. Returns oldest-first (same order as ring buffer pages).
pub fn query_bars_before(
    data_dir: &Path,
    symbol: &str,
    timeframe: &str,
    before_ms: i64,
    limit: usize,
) -> anyhow::Result<Vec<BarRecord>> {
    let files = find_parquet_files(data_dir, symbol, Some(timeframe), None);
    if files.is_empty() {
        debug!(symbol, timeframe, "duckdb fallback: no parquet files found");
        return Ok(vec![]);
    }
    let parquet_expr = build_parquet_expr(&files);
    let sql = format!(
        "SELECT timestamp, open, high, low, close, volume \
         FROM {parquet_expr} \
         WHERE timestamp < {before_ms} \
         ORDER BY timestamp DESC \
         LIMIT {limit}"
    );
    let mut bars = run_bar_query(&sql)?;
    bars.reverse(); // DESC → oldest-first
    debug!(symbol, timeframe, count = bars.len(), "duckdb fallback: before query done");
    Ok(bars)
}

/// Query bars in `[from_ms, to_ms)` from Parquet files, oldest-first.
///
/// Used by the historical indicator compute path — returns `alm_core::Bar`
/// so callers can feed bars directly through `IndicatorBox::update`.
pub fn query_bars_for_compute(
    data_dir: &Path,
    parquet_symbol: &str,
    live_symbol: &str,
    timeframe: &str,
    from_ms: i64,
    to_ms: i64,
    limit: usize,
) -> anyhow::Result<Vec<Bar>> {
    let files = find_parquet_files(data_dir, parquet_symbol, Some(timeframe), None);
    if files.is_empty() {
        return Ok(vec![]);
    }
    let parquet_expr = build_parquet_expr(&files);
    let sql = format!(
        "SELECT timestamp, open, high, low, close, volume \
         FROM {parquet_expr} \
         WHERE timestamp >= {from_ms} AND timestamp < {to_ms} \
         ORDER BY timestamp ASC \
         LIMIT {limit}"
    );
    let conn = duckdb::Connection::open_in_memory()
        .map_err(|e| anyhow::anyhow!("duckdb open: {e}"))?;
    let mut stmt = conn.prepare(&sql)
        .map_err(|e| anyhow::anyhow!("duckdb prepare: {e}"))?;
    let sym = live_symbol.to_string();
    let bars = stmt
        .query_map([], |row| {
            Ok(Bar {
                timestamp:    row.get(0)?,
                symbol:       sym.clone(),
                open:         row.get(1)?,
                high:         row.get(2)?,
                low:          row.get(3)?,
                close:        row.get(4)?,
                volume:       row.get(5)?,
                vwap:         None,
                transactions: None,
            })
        })
        .map_err(|e| anyhow::anyhow!("duckdb query: {e}"))?
        .map(|r| r.map_err(|e| anyhow::anyhow!("{e}")))
        .collect::<anyhow::Result<_>>()?;
    Ok(bars)
}

fn build_parquet_expr(files: &[std::path::PathBuf]) -> String {
    let list = files.iter()
        .map(|p| format!("'{}'", p.display()))
        .collect::<Vec<_>>()
        .join(", ");
    format!("read_parquet([{list}])")
}

fn run_bar_query(sql: &str) -> anyhow::Result<Vec<BarRecord>> {
    let conn = duckdb::Connection::open_in_memory()
        .map_err(|e| anyhow::anyhow!("duckdb open: {e}"))?;
    let mut stmt = conn.prepare(sql)
        .map_err(|e| anyhow::anyhow!("duckdb prepare: {e}"))?;
    let bars = stmt
        .query_map([], |row| {
            Ok(BarRecord {
                t: row.get(0)?,
                o: row.get(1)?,
                h: row.get(2)?,
                l: row.get(3)?,
                c: row.get(4)?,
                v: row.get(5)?,
                vwap: None,
                n: None,
            })
        })
        .map_err(|e| anyhow::anyhow!("duckdb query: {e}"))?
        .map(|r| r.map_err(|e| anyhow::anyhow!("{e}")))
        .collect::<anyhow::Result<_>>()?;
    Ok(bars)
}
