use std::path::{Path, PathBuf};

use anyhow::{Context, Result};
use alm_core::Bar;
use alm_data::{BarFeed, InMemoryFeed, ParquetFeed};
use chrono::{NaiveDate, TimeZone, Timelike};
use chrono_tz::America::New_York;
use chrono_tz::Asia::Ho_Chi_Minh;
use walkdir::WalkDir;

/// Walk `data_dir` and collect all `.parquet` files located inside any
/// directory whose name matches `symbol` (case-insensitive).
///
/// When `timeframe` is `Some("H1")` (or any candle-type directory name), only
/// files whose grandparent directory matches that string are included.  This
/// prevents mixing bars from different timeframes when the on-disk layout is
/// `{exchange}/{timeframe}/{symbol}/*.parquet`.
pub fn find_parquet_files(data_dir: &Path, symbol: &str, timeframe: Option<&str>) -> Vec<PathBuf> {
    let symbol_lower    = symbol.to_lowercase();
    let timeframe_lower = timeframe.map(|t| t.to_lowercase());

    let mut files: Vec<PathBuf> = WalkDir::new(data_dir)
        .follow_links(false)
        .into_iter()
        .filter_map(|e| e.ok())
        .filter(|e| {
            // File must be .parquet
            if !e.file_type().is_file() {
                return false;
            }
            let path = e.path();
            if path.extension().and_then(|s| s.to_str()) != Some("parquet") {
                return false;
            }
            // Skip EOD directories (e.g. crypto_eod, vn_eod)
            let in_eod = path.components().any(|c| {
                c.as_os_str().to_str().map(|s| s.ends_with("_eod")).unwrap_or(false)
            });
            if in_eod {
                return false;
            }
            // Parent directory name must match symbol
            let parent = path.parent();
            let parent_matches = parent
                .and_then(|p| p.file_name())
                .and_then(|n| n.to_str())
                .map(|n| n.to_lowercase() == symbol_lower)
                .unwrap_or(false);
            if !parent_matches {
                return false;
            }
            // Grandparent directory must match timeframe when specified
            if let Some(ref tf) = timeframe_lower {
                let grandparent_matches = parent
                    .and_then(|p| p.parent())
                    .and_then(|p| p.file_name())
                    .and_then(|n| n.to_str())
                    .map(|n| n.to_lowercase() == *tf)
                    .unwrap_or(false);
                if !grandparent_matches {
                    return false;
                }
            }
            true
        })
        .map(|e| e.into_path())
        .collect();

    files.sort(); // sort by filename for chronological order
    files
}

/// Returns true if the bar falls within regular trading hours for the given exchange.
/// `exchange`: `"us"` → NYSE 09:30–16:00 ET; `"vn"` → HOSE 09:00–11:30 & 13:00–14:45 ICT.
pub fn is_market_hours(ts_ms: i64, exchange: &str) -> bool {
    match exchange {
        "vn" => {
            let dt = Ho_Chi_Minh.timestamp_millis_opt(ts_ms).single();
            match dt {
                Some(dt) => {
                    let m = dt.hour() * 60 + dt.minute();
                    // Morning: 09:00–11:30 | Afternoon: 13:00–14:45
                    (m >= 9 * 60 && m < 11 * 60 + 30) || (m >= 13 * 60 && m < 14 * 60 + 45)
                }
                None => false,
            }
        }
        _ => {
            // Default: NYSE 09:30–16:00 ET
            let dt = New_York.timestamp_millis_opt(ts_ms).single();
            match dt {
                Some(dt) => {
                    let m = dt.hour() * 60 + dt.minute();
                    m >= 9 * 60 + 30 && m < 16 * 60
                }
                None => false,
            }
        }
    }
}

/// Parse "YYYY-MM-DD" → Unix milliseconds (start of day UTC).
/// Returns `None` if the string is empty or unparseable.
pub fn parse_date_ms(date_str: &str) -> Option<i64> {
    NaiveDate::parse_from_str(date_str, "%Y-%m-%d")
        .ok()
        .map(|d| d.and_hms_opt(0, 0, 0).unwrap())
        .map(|dt| dt.and_utc().timestamp_millis())
}

/// Load bars from the given Parquet files, optionally filtered to [from_ms, to_ms].
/// Returns an `InMemoryFeed` ready for the engine.
pub fn load_bars(
    files: &[PathBuf],
    symbol: &str,
    from_ms: Option<i64>,
    to_ms: Option<i64>,
    market_hours_only: bool,
    exchange: &str,
) -> Result<InMemoryFeed> {
    if files.is_empty() {
        anyhow::bail!("no parquet files found for symbol '{}'", symbol);
    }

    let paths: Vec<&Path> = files.iter().map(PathBuf::as_path).collect();
    // Push date range into Polars before materializing — enables Parquet row-group pruning.
    let feed = ParquetFeed::load_many_filtered(&paths, symbol, from_ms, to_ms)
        .with_context(|| format!("loading parquet data for '{}'", symbol))?;

    // Drain bars — date range already applied; only market_hours needs post-filtering.
    let mut all_bars: Vec<Bar> = Vec::new();
    let mut feed = feed;
    while let Some(bar) = feed.next() {
        let keep = !market_hours_only || is_market_hours(bar.timestamp, exchange);
        if keep {
            all_bars.push(bar);
        }
    }

    if all_bars.is_empty() {
        anyhow::bail!(
            "no bars for '{}' in the requested date range",
            symbol
        );
    }

    Ok(InMemoryFeed::new(all_bars, symbol.to_string()))
}
