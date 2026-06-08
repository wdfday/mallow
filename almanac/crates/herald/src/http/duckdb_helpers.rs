//! DuckDB core query logic and parquet fallback helpers.
//!
//! These functions are called from:
//! - `data/unified.rs` (transparent parquet fallback)
//!
//! Parquet column layout (written by hist-data / alm-data): t, o, h, l, c, v

use std::path::Path;

use tracing::{debug, warn};
use metrics::{counter, histogram};

use alm_core::Bar;
use alm_engine::data::find_parquet_files;

use super::types::BarRecord;

// ── Parquet fallback helpers ──────────────────────────────────────────────────

/// Query bars with `t < before_ms` from Parquet files, newest `limit` bars.
/// Returns oldest-first (same order as ring buffer pages).
///
/// If no parquet files exist for the requested timeframe and it is not M1,
/// falls back to M1 files and resamples via DuckDB GROUP BY bucket.
pub fn query_bars_before(
    data_dir: &Path,
    symbol: &str,
    timeframe: &str,
    before_ms: i64,
    limit: usize,
) -> anyhow::Result<Vec<BarRecord>> {
    let files = find_parquet_files(data_dir, symbol, Some(timeframe), None);
    if files.is_empty() {
        if timeframe.to_uppercase() != "M1" {
            return query_bars_before_resampled(data_dir, symbol, timeframe, before_ms, limit);
        }
        warn!(
            symbol, timeframe,
            data_dir = %data_dir.display(),
            "duckdb fallback: no parquet files found"
        );
        return Ok(vec![]);
    }
    let parquet_expr = build_parquet_expr(&files);
    let sql = format!(
        "SELECT t, o, h, l, c, v \
         FROM {parquet_expr} \
         WHERE t < {before_ms} \
         ORDER BY t DESC \
         LIMIT {limit}"
    );
    let mut bars = run_bar_query(&sql)?;
    bars.reverse(); // DESC → oldest-first
    detect_bar_gaps(&bars, timeframe, symbol, "duckdb before");
    debug!(symbol, timeframe, count = bars.len(), file_count = files.len(), "duckdb fallback: before query done");
    Ok(bars)
}

/// Detect timestamp gaps between consecutive bars and log a `warn!` for each.
/// `interval_ms` is derived from the timeframe label; if unknown we skip.
fn detect_bar_gaps(bars: &[BarRecord], timeframe: &str, symbol: &str, source: &str) {
    let interval_ms = timeframe_to_ms(timeframe);
    if interval_ms == 0 || bars.len() < 2 {
        return;
    }
    let mut gap_count = 0usize;
    for w in bars.windows(2) {
        let gap_ms = w[1].t - w[0].t;
        if gap_ms > interval_ms * 2 {
            gap_count += 1;
            warn!(
                symbol, timeframe, source,
                bar0_ts = w[0].t,
                bar1_ts = w[1].t,
                gap_ms,
                expected_ms = interval_ms,
                "duckdb result: gap between consecutive bars",
            );
        }
    }
    if gap_count > 0 {
        warn!(
            symbol, timeframe, source,
            gap_count, total_bars = bars.len(),
            "duckdb result: gaps found in returned bars",
        );
    }
}

/// Same as [`detect_bar_gaps`] but for `alm_core::Bar` slices (compute path).
fn detect_core_bar_gaps(bars: &[Bar], timeframe: &str, symbol: &str, source: &str) {
    let interval_ms = timeframe_to_ms(timeframe);
    if interval_ms == 0 || bars.len() < 2 {
        return;
    }
    let mut gap_count = 0usize;
    for w in bars.windows(2) {
        let gap_ms = w[1].timestamp - w[0].timestamp;
        if gap_ms > interval_ms * 2 {
            gap_count += 1;
            warn!(
                symbol, timeframe, source,
                bar0_ts = w[0].timestamp,
                bar1_ts = w[1].timestamp,
                gap_ms,
                expected_ms = interval_ms,
                "duckdb compute result: gap between consecutive bars",
            );
        }
    }
    if gap_count > 0 {
        warn!(
            symbol, timeframe, source,
            gap_count, total_bars = bars.len(),
            "duckdb compute result: gaps found in returned bars",
        );
    }
}

/// Resample M1 parquet files to the requested timeframe when no dedicated
/// parquet files exist for that timeframe.
fn query_bars_before_resampled(
    data_dir: &Path,
    symbol: &str,
    timeframe: &str,
    before_ms: i64,
    limit: usize,
) -> anyhow::Result<Vec<BarRecord>> {
    let m1_files = find_parquet_files(data_dir, symbol, Some("M1"), None);
    if m1_files.is_empty() {
        warn!(
            symbol, timeframe,
            data_dir = %data_dir.display(),
            "duckdb resample: no M1 files found either"
        );
        return Ok(vec![]);
    }
    let interval_ms = timeframe_to_ms(timeframe);
    if interval_ms == 0 {
        warn!(symbol, timeframe, "duckdb resample: unknown timeframe");
        return Ok(vec![]);
    }
    // Fetch enough M1 history to fill `limit` buckets
    let from_ms = before_ms - (limit as i64 + 1) * interval_ms;
    let parquet_expr = build_parquet_expr(&m1_files);
    // min_by(o, t) → open of the first M1 bar in the bucket
    // max_by(c, t) → close of the last M1 bar in the bucket
    let sql = format!(
        "SELECT \
          (t / {interval_ms}) * {interval_ms} AS t, \
          min_by(o, t) AS o, \
          max(h)       AS h, \
          min(l)       AS l, \
          max_by(c, t) AS c, \
          sum(v)       AS v \
         FROM {parquet_expr} \
         WHERE t >= {from_ms} AND t < {before_ms} \
         GROUP BY (t / {interval_ms}) * {interval_ms} \
         ORDER BY 1 DESC \
         LIMIT {limit}"
    );
    let mut bars = run_bar_query(&sql)?;
    bars.reverse(); // DESC → oldest-first
    detect_bar_gaps(&bars, timeframe, symbol, "duckdb resample");
    debug!(symbol, timeframe, count = bars.len(), "duckdb resample: done");
    Ok(bars)
}

fn timeframe_to_ms(tf: &str) -> i64 {
    match tf.to_uppercase().as_str() {
        "M1"  =>          60_000,
        "M3"  =>         180_000,
        "M5"  =>         300_000,
        "M10" =>         600_000,
        "M15" =>         900_000,
        "M30" =>       1_800_000,
        "H1"  =>       3_600_000,
        "H2"  =>       7_200_000,
        "H4"  =>      14_400_000,
        "H6"  =>      21_600_000,
        "H12" =>      43_200_000,
        "D1"  =>      86_400_000,
        "W1"  =>     604_800_000,
        _     => 0,
    }
}

/// Query bars in `[from_ms, to_ms)` from Parquet files, oldest-first.
///
/// Used by the historical indicator compute path — returns `alm_core::Bar`
/// so callers can feed bars directly through `IndicatorBox::update`.
///
/// Falls back to M1 files + DuckDB GROUP BY resampling when no dedicated
/// parquet files exist for the requested timeframe.
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
    let (files, resample) = if files.is_empty() && timeframe.to_uppercase() != "M1" {
        let m1 = find_parquet_files(data_dir, parquet_symbol, Some("M1"), None);
        (m1, true)
    } else {
        (files, false)
    };
    if files.is_empty() {
        return Ok(vec![]);
    }
    let compute_start = std::time::Instant::now();
    let parquet_expr = build_parquet_expr(&files);
    let sql = if resample {
        let interval_ms = timeframe_to_ms(timeframe);
        if interval_ms == 0 { return Ok(vec![]); }
        format!(
            "SELECT \
              (t / {interval_ms}) * {interval_ms} AS t, \
              min_by(o, t) AS o, \
              max(h)       AS h, \
              min(l)       AS l, \
              max_by(c, t) AS c, \
              sum(v)       AS v \
             FROM {parquet_expr} \
             WHERE t >= {from_ms} AND t < {to_ms} \
             GROUP BY (t / {interval_ms}) * {interval_ms} \
             ORDER BY 1 ASC \
             LIMIT {limit}"
        )
    } else {
        format!(
            "SELECT t, o, h, l, c, v \
             FROM {parquet_expr} \
             WHERE t >= {from_ms} AND t < {to_ms} \
             ORDER BY t ASC \
             LIMIT {limit}"
        )
    };
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
        .collect::<anyhow::Result<Vec<Bar>>>()?;
    detect_core_bar_gaps(&bars, timeframe, parquet_symbol, "duckdb compute");
    histogram!("herald_duckdb_query_duration_ms", "kind" => "compute").record(compute_start.elapsed().as_millis() as f64);
    counter!("herald_duckdb_queries_total", "kind" => "compute").increment(1);
    Ok(bars)
}

fn build_parquet_expr(files: &[std::path::PathBuf]) -> String {
    let list = files.iter()
        .map(|p| {
            // Normalise separators (Windows) and escape single quotes inside the path.
            let s = p.to_string_lossy()
                .replace('\\', "/")
                .replace('\'', "\\'");
            format!("'{s}'")
        })
        .collect::<Vec<_>>()
        .join(", ");
    format!("read_parquet([{list}])")
}

fn run_bar_query(sql: &str) -> anyhow::Result<Vec<BarRecord>> {
    let start = std::time::Instant::now();
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
    histogram!("herald_duckdb_query_duration_ms", "kind" => "before").record(start.elapsed().as_millis() as f64);
    counter!("herald_duckdb_queries_total", "kind" => "before").increment(1);
    Ok(bars)
}
