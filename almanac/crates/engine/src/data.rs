//! Historical bar loading from Parquet files for the backtest runner.
//!
//! Moved from the former `logbook` crate. Exposes three helpers:
//! [`find_parquet_files`], [`parse_date_ms`] and [`load_bars`] — all pure
//! IO with no engine or strategy dependency so callers can also use them
//! for ad-hoc data inspection.
//!
//! # On-disk layout (`mallow/data/`)
//!
//! ```text
//! data/
//! ├── BinanceFlat/          crypto — downloaded from data.binance.vision (no API key)
//! │   ├── M1/{SYMBOL}/      monthly parquet files, e.g. BTCUSDT_M1_2024-01.parquet
//! │   ├── M5 … H4/{SYMBOL}/
//! │   └── D1/{SYMBOL}/      single all-history file per symbol
//! │   Symbols: BTCUSDT ETHUSDT BNBUSDT SOLUSDT XRPUSDT ADAUSDT
//! │            AVAXUSDT DOTUSDT LINKUSDT POLUSDT
//! │   (MATICUSDT files still on disk — renamed to POLUSDT Sep 2024)
//! │
//! ├── Polygon/              US stocks — Polygon.io
//! │   ├── M1 … H4/{SYMBOL}/
//! │   Symbols: AAPL AMZN MSFT GOOGL NVDA TSLA AMD … (Nasdaq-100 subset)
//! │
//! └── VCI/                  Vietnam stocks — VCI
//!     ├── M1 … D1/{SYMBOL}/
//!     Symbols: VCB VHM HPG FPT MBB TCB … (VN30 subset)
//! ```
//!
//! `find_parquet_files(data_dir, symbol, timeframe, data_source)` walks this
//! tree and filters by directory name match — provider is the top-level dir,
//! timeframe is the second level, symbol is the third.

use std::path::{Path, PathBuf};

use alm_core::Bar;
use alm_data::{BarFeed, BarVecFeed, ParquetFeed};
use anyhow::{Context, Result};
use chrono::{NaiveDate, TimeZone, Timelike};
use chrono_tz::America::New_York;
use chrono_tz::Asia::Ho_Chi_Minh;
use walkdir::WalkDir;

/// Derive the market-hours region from a data source name.
///
/// - `"polygon"` → `"us"` (NYSE/NASDAQ hours)
/// - `"vci"` → `"vn"` (HOSE hours)
/// - `"bnb"` / `"okx"` / anything else → `""` (24/7, no filter)
pub fn market_region_from_data_source(data_source: &str) -> &'static str {
    match data_source {
        "polygon" => "us",
        "vci" => "vn",
        _ => "",
    }
}

/// Walk `data_dir` and collect all `.parquet` files located inside any
/// directory whose name matches `symbol` (case-insensitive).
///
/// When `timeframe` is `Some("H1")` (or any candle-type directory name),
/// only files whose grandparent directory matches that string are included.
///
/// When `data_source` is `Some("polygon")` (or any provider name), only
/// files inside a directory component matching that name are included —
/// this prevents mixing bars from different providers when the on-disk
/// layout is `{data_source}/{timeframe}/{symbol}/*.parquet`.
pub fn find_parquet_files(
    data_dir: &Path,
    symbol: &str,
    timeframe: Option<&str>,
    data_source: Option<&str>,
) -> Vec<PathBuf> {
    let symbol_lower = symbol.to_lowercase();
    let timeframe_lower = timeframe.map(|t| t.to_lowercase());
    let data_source_lower = data_source.map(|s| s.to_lowercase());

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
            // Skip EOD directories (e.g. crypto_eod, vn_eod).
            let in_eod = path.components().any(|c| {
                c.as_os_str()
                    .to_str()
                    .map(|s| s.ends_with("_eod"))
                    .unwrap_or(false)
            });
            if in_eod {
                return false;
            }
            let parent = path.parent();
            let parent_matches = parent
                .and_then(|p| p.file_name())
                .and_then(|n| n.to_str())
                .map(|n| n.to_lowercase() == symbol_lower)
                .unwrap_or(false);
            if !parent_matches {
                return false;
            }
            if let Some(ref src) = data_source_lower {
                let has_src = path.components().any(|c| {
                    c.as_os_str()
                        .to_str()
                        .map(|s| s.to_lowercase() == *src)
                        .unwrap_or(false)
                });
                if !has_src {
                    return false;
                }
            }
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

    files.sort_by_key(|p| {
        p.file_name()
            .and_then(|n| n.to_str())
            .map(|s| s.to_owned())
            .unwrap_or_default()
    });
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
                    // Morning 09:00–11:30 | Afternoon 13:00–14:45.
                    (m >= 9 * 60 && m < 11 * 60 + 30) || (m >= 13 * 60 && m < 14 * 60 + 45)
                }
                None => false,
            }
        }
        _ => {
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

/// Scan `files` for the latest monthly parquet (`SYMBOL_TF_YYYY-MM.parquet`)
/// and return the start of that month as Unix milliseconds (UTC).
///
/// Falls back to `None` when no monthly file is found (e.g. fresh install with
/// only daily or range files). The caller should then fall back to a fixed
/// lookback window (`HERALD_WARM_DAYS`).
pub fn find_bootstrap_from_ms(files: &[PathBuf]) -> Option<i64> {
    let mut latest: Option<(i32, u32)> = None; // (year, month)

    for f in files {
        let stem = f
            .file_name()
            .and_then(|n| n.to_str())
            .and_then(|n| n.strip_suffix(".parquet"))?;

        // Monthly file: last '_'-segment is exactly "YYYY-MM" (7 chars).
        // Daily file:   last segment is "YYYY-MM-DD" (10 chars).
        // Range file:   last segment is "YYYY-MM-DD" after "_to_".
        let date_part = stem.rsplit('_').next()?;
        if date_part.len() != 7 {
            continue;
        }
        let (y, m) = date_part.split_once('-')?;
        let year: i32 = y.parse().ok()?;
        let month: u32 = m.parse().ok()?;
        if month == 0 || month > 12 {
            continue;
        }
        match latest {
            None => latest = Some((year, month)),
            Some((ey, em)) if (year, month) > (ey, em) => latest = Some((year, month)),
            _ => {}
        }
    }

    latest.and_then(|(year, month)| {
        NaiveDate::from_ymd_opt(year, month, 1)
            .and_then(|d| d.and_hms_opt(0, 0, 0))
            .map(|dt| dt.and_utc().timestamp_millis())
    })
}

/// Parse `"YYYY-MM-DD"` → Unix milliseconds (start of day UTC).
pub fn parse_date_ms(date_str: &str) -> Option<i64> {
    NaiveDate::parse_from_str(date_str, "%Y-%m-%d")
        .ok()
        .map(|d| d.and_hms_opt(0, 0, 0).unwrap())
        .map(|dt| dt.and_utc().timestamp_millis())
}

/// Load bars from the given Parquet files, optionally filtered to
/// `[from_ms, to_ms]`. Returns an [`BarVecFeed`] ready for the engine.
pub fn load_bars(
    files: &[PathBuf],
    symbol: &str,
    from_ms: Option<i64>,
    to_ms: Option<i64>,
    market_hours_only: bool,
    exchange: &str,
) -> Result<BarVecFeed> {
    if files.is_empty() {
        anyhow::bail!("no parquet files found for symbol '{}'", symbol);
    }

    let paths: Vec<&Path> = files.iter().map(PathBuf::as_path).collect();
    // Push date range into Polars before materializing — enables row-group pruning.
    let feed = ParquetFeed::load_many_filtered(&paths, symbol, from_ms, to_ms)
        .with_context(|| format!("loading parquet data for '{}'", symbol))?;

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

    Ok(BarVecFeed::new(all_bars, symbol.to_string()))
}
