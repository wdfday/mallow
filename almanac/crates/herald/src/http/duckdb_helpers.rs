//! DuckDB core query logic and parquet fallback helpers.
//!
//! These functions are called from:
//! - `data/ohlcv.rs` and `data/unified.rs` (transparent parquet fallback)
//! - `data/duckdb.rs` (ad-hoc SQL handler)

use std::path::Path;
use std::sync::Arc;

use duckdb::types::ValueRef;
use serde::Deserialize;
use tracing::{debug, info};

use alm_core::Bar;
use alm_engine::data::{find_parquet_files, parse_date_ms};

use super::types::BarRecord;

// ── Request ───────────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
pub struct DuckQueryReq {
    pub symbol: String,
    #[serde(default)]
    pub timeframe: Option<String>,
    #[serde(default)]
    pub data_source: Option<String>,
    /// Inclusive start date as "YYYY-MM-DD".
    #[serde(default)]
    pub from: Option<String>,
    /// Inclusive end date as "YYYY-MM-DD".
    #[serde(default)]
    pub to: Option<String>,
    /// Custom SELECT statement. Use `bars` as the source table.
    /// When None the default OHLCV projection with optional time filter is used.
    #[serde(default)]
    pub sql: Option<String>,
}

// ── Core query logic (blocking) ───────────────────────────────────────────────

pub fn run_query(data_dir: &std::path::Path, req: DuckQueryReq) -> anyhow::Result<serde_json::Value> {
    // Locate parquet files matching the request.
    let files = find_parquet_files(
        data_dir,
        &req.symbol,
        req.timeframe.as_deref(),
        req.data_source.as_deref(),
    );
    anyhow::ensure!(
        !files.is_empty(),
        "no parquet files found for symbol '{}' (timeframe={:?}, data_source={:?})",
        req.symbol, req.timeframe, req.data_source
    );

    info!(symbol = %req.symbol, files = files.len(), "duckdb: found parquet files");

    // Build the read_parquet([...]) expression.
    let file_list = files.iter()
        .map(|p| format!("'{}'", p.display()))
        .collect::<Vec<_>>()
        .join(", ");
    let parquet_expr = format!("read_parquet([{file_list}])");

    // Resolve optional time range.
    let from_ms = req.from.as_deref().and_then(parse_date_ms);
    // "to" is end-of-day inclusive.
    let to_ms = req.to.as_deref().and_then(|s| parse_date_ms(s).map(|ms| ms + 86_400_000 - 1));

    // Build the final SQL string.
    let sql = if let Some(user_sql) = req.sql {
        validate_custom_sql(&user_sql)?;
        format!("WITH bars AS (SELECT * FROM {parquet_expr})\n{user_sql}")
    } else {
        let mut where_parts: Vec<String> = Vec::new();
        if let Some(from) = from_ms { where_parts.push(format!("timestamp >= {from}")); }
        if let Some(to)   = to_ms   { where_parts.push(format!("timestamp <= {to}"));   }
        let where_clause = if where_parts.is_empty() {
            String::new()
        } else {
            format!("WHERE {}", where_parts.join(" AND "))
        };
        format!(
            "SELECT timestamp, symbol, open, high, low, close, volume \
             FROM {parquet_expr} {where_clause} ORDER BY timestamp"
        )
    };

    // Run query with a fresh in-memory DuckDB connection.
    let conn = duckdb::Connection::open_in_memory()
        .map_err(|e| anyhow::anyhow!("duckdb open: {e}"))?;

    let mut stmt = conn.prepare(&sql)
        .map_err(|e| anyhow::anyhow!("duckdb prepare: {e}"))?;

    let n_cols = stmt.column_count();
    let col_names: Vec<String> = (0..n_cols)
        .map(|i| stmt.column_name(i).map(ToString::to_string).unwrap_or_default())
        .collect();

    let rows: Vec<serde_json::Value> = stmt
        .query_map([], |row| {
            let mut obj = serde_json::Map::with_capacity(n_cols);
            for (i, name) in col_names.iter().enumerate() {
                obj.insert(name.clone(), vref_to_json(row, i));
            }
            Ok(obj)
        })
        .map_err(|e| anyhow::anyhow!("duckdb query: {e}"))?
        .map(|r| r.map(serde_json::Value::Object).map_err(|e| anyhow::anyhow!("{e}")))
        .collect::<anyhow::Result<_>>()?;

    info!(symbol = %req.symbol, rows = rows.len(), "duckdb: query complete");
    Ok(serde_json::json!({ "count": rows.len(), "rows": rows }))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Reject custom SQL that tries to open files directly or run DML.
pub fn validate_custom_sql(sql: &str) -> anyhow::Result<()> {
    let lower = sql.trim().to_lowercase();
    anyhow::ensure!(
        lower.starts_with("select"),
        "custom SQL must be a SELECT statement"
    );
    for banned in &["read_parquet", "read_csv", "read_json", "glob(", "copy ", "export "] {
        anyhow::ensure!(
            !lower.contains(banned),
            "custom SQL may not use '{banned}' — reference 'bars' as the source table"
        );
    }
    Ok(())
}

// ── Parquet fallback helpers (called from data/ohlcv.rs and data/unified.rs) ──

/// Query bars with `timestamp < before_ms` from Parquet files, newest `limit`
/// bars. Returns oldest-first (same order as ring buffer pages).
///
/// `symbol` must match the on-disk directory name (Binance-style, no dashes).
/// `timeframe` is the standard herald string e.g. `"M1"`, `"H4"`.
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
/// `live_symbol` is the symbol as used in the ledger (may contain dashes,
/// e.g. `"BTC-USDT"`); `parquet_symbol` is the on-disk name (`"BTCUSDT"`).
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

/// Query bars with `timestamp > after_ms` from Parquet files, oldest `limit`
/// bars. Returns oldest-first.
///
/// `symbol` must match the on-disk directory name (Binance-style, no dashes).
pub fn query_bars_after(
    data_dir: &Path,
    symbol: &str,
    timeframe: &str,
    after_ms: i64,
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
         WHERE timestamp > {after_ms} \
         ORDER BY timestamp ASC \
         LIMIT {limit}"
    );
    let bars = run_bar_query(&sql)?;
    debug!(symbol, timeframe, count = bars.len(), "duckdb fallback: after query done");
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

/// Convert a DuckDB column value to `serde_json::Value`.
pub fn vref_to_json(row: &duckdb::Row, idx: usize) -> serde_json::Value {
    let vr = match row.get_ref(idx) {
        Ok(v)  => v,
        Err(_) => return serde_json::Value::Null,
    };
    match vr {
        ValueRef::Null          => serde_json::Value::Null,
        ValueRef::Boolean(b)    => b.into(),
        ValueRef::TinyInt(i)    => i.into(),
        ValueRef::SmallInt(i)   => i.into(),
        ValueRef::Int(i)        => i.into(),
        ValueRef::BigInt(i)     => i.into(),
        ValueRef::HugeInt(i)    => (i as i64).into(),
        ValueRef::UTinyInt(i)   => i.into(),
        ValueRef::USmallInt(i)  => i.into(),
        ValueRef::UInt(i)       => i.into(),
        ValueRef::UBigInt(i)    => i.into(),
        ValueRef::Float(f)      => (f as f64).into(),
        ValueRef::Double(f)     => {
            if f.is_finite() { f.into() } else { serde_json::Value::Null }
        }
        ValueRef::Text(s)       => std::str::from_utf8(s).unwrap_or("").into(),
        ValueRef::Blob(_)       => serde_json::Value::Null,
        _                       => serde_json::Value::Null,
    }
}
