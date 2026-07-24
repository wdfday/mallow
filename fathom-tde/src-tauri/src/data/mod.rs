pub mod backtest;
pub mod catalog;
pub mod home;
pub mod loader;
pub mod registry;

use std::path::Path;

use alm_core::{Bar, Timeframe};
use alm_data::{feed::BarFeed, parquet::ParquetFeed};
use serde::Serialize;

/// Mirrors `almanac-wasm.ts`'s `OhlcvBar` — `time` is Unix **seconds**, matching the convention
/// used everywhere else in the frontend (mock data, backtest.ts, chart rendering).
#[derive(Debug, Clone, Serialize)]
pub struct OhlcvBar {
    pub time: i64,
    pub open: f64,
    pub high: f64,
    pub low: f64,
    pub close: f64,
    pub volume: f64,
}

impl From<&Bar> for OhlcvBar {
    fn from(bar: &Bar) -> Self {
        OhlcvBar {
            time: bar.timestamp / 1000,
            open: bar.open,
            high: bar.high,
            low: bar.low,
            close: bar.close,
            volume: bar.volume,
        }
    }
}

fn drain_feed(mut feed: impl BarFeed) -> Vec<OhlcvBar> {
    let mut bars = Vec::with_capacity(feed.len());
    while let Some(bar) = feed.next() {
        bars.push(OhlcvBar::from(&bar));
    }
    bars
}

/// Direct single-file load — kept for ad-hoc use (e.g. previewing an arbitrary file);
/// the chart path goes through [`load_bars`]'s tiered resolution instead.
#[tauri::command]
pub fn load_ohlcv(path: String, symbol: String) -> Result<Vec<OhlcvBar>, String> {
    let feed = ParquetFeed::load(Path::new(&path), symbol)
        .map_err(|e| format!("failed to load parquet {path}: {e}"))?;
    Ok(drain_feed(feed))
}

#[tauri::command]
pub fn load_ohlcv_csv(path: String, symbol: String) -> Result<Vec<OhlcvBar>, String> {
    let bars = read_csv_bars(Path::new(&path), &symbol)?;
    Ok(bars.iter().map(OhlcvBar::from).collect())
}

/// Chart-facing tiered load: project `data/` → mounts → `~/Fathom/.data` lake, with
/// TF resampling when only a finer TF exists. `None` = symbol not found anywhere
/// (FE falls back to mock, flagged as such).
#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct LoadBarsResult {
    pub bars: Vec<OhlcvBar>,
    /// e.g. `"lake:binanceflat"`, `"project:BTCUSDT.csv"`
    pub source: String,
    pub file_timeframe: String,
    pub resampled: bool,
    /// True when `bars` starts at the earliest data this root actually has — i.e. there is
    /// nothing further back to page for (a `before` request that returned fewer than requested,
    /// or an unbounded request that returned everything on disk). The chart's "load back" scroll
    /// handler stops calling `load_bars` again for this symbol once it sees this.
    pub exhausted: bool,
}

/// `before_ms`: when set, only bars with `timestamp <= before_ms` are considered — the "load
/// back" pagination cursor (ChartPanel passes the oldest currently-loaded bar's time minus 1ms,
/// so the boundary bar itself isn't duplicated). `None` = the normal initial load (most recent
/// `limit` bars).
#[tauri::command(async)]
pub fn load_bars(
    project_path: Option<String>,
    symbol: String,
    timeframe: Option<String>,
    limit: Option<usize>,
    before_ms: Option<i64>,
) -> Result<Option<LoadBarsResult>, String> {
    let tf_str = timeframe.unwrap_or_else(|| "M1".into()).to_uppercase();
    let tf: Timeframe = tf_str.parse().map_err(|_| format!("invalid timeframe: {tf_str}"))?;
    let Some(resolved) = catalog::resolve_bars(project_path.as_deref(), &symbol, tf, None, before_ms)? else {
        eprintln!(
            "[load_bars] {symbol} {tf_str} — no local source resolved (project={:?}); falling through to herald/mock",
            project_path
        );
        return Ok(None);
    };
    eprintln!("[load_bars] {symbol} {tf_str} -> {} ({} bars)", resolved.source, resolved.bars.len());
    let mut bars: Vec<OhlcvBar> = resolved.bars.iter().map(OhlcvBar::from).collect();
    let requested = limit.unwrap_or(bars.len());
    // Fewer bars available than asked for ⇒ definitely nothing further back. Getting exactly
    // `requested` (or more, pre-truncation) doesn't prove there's more, but treating it as
    // "maybe more" is the safe direction — a following call returning 0 (or short) settles it.
    let exhausted = bars.len() < requested;
    if bars.len() > requested {
        bars.drain(..bars.len() - requested);
    }
    Ok(Some(LoadBarsResult {
        bars,
        source: resolved.source,
        file_timeframe: resolved.file_timeframe.to_string(),
        resampled: resolved.resampled,
        exhausted,
    }))
}

/// Parse an OHLCV CSV (header required, flexible column names) into ms-timestamped [`Bar`]s.
/// Shared by the legacy `load_ohlcv_csv` command and the catalog's flat-file resolution.
pub fn read_csv_bars(path: &Path, symbol: &str) -> Result<Vec<Bar>, String> {
    let mut reader = csv::ReaderBuilder::new()
        .has_headers(true)
        .from_path(path)
        .map_err(|e| format!("failed to open csv {}: {e}", path.display()))?;

    let headers = reader.headers().map_err(|e| e.to_string())?.clone();
    let col = |names: &[&str]| -> Option<usize> {
        headers.iter().position(|h| names.contains(&h.to_lowercase().as_str()))
    };
    let time_idx = col(&["time", "timestamp", "date"]).ok_or("csv missing a time/date column")?;
    let open_idx = col(&["open", "o"]).ok_or("csv missing an open column")?;
    let high_idx = col(&["high", "h"]).ok_or("csv missing a high column")?;
    let low_idx = col(&["low", "l"]).ok_or("csv missing a low column")?;
    let close_idx = col(&["close", "c"]).ok_or("csv missing a close column")?;
    let vol_idx = col(&["volume", "v", "vol"]);

    let mut bars = Vec::new();
    for record in reader.records() {
        let record = record.map_err(|e| e.to_string())?;
        let time_raw = record.get(time_idx).unwrap_or_default();
        let time_ms = parse_time_ms(time_raw)
            .ok_or_else(|| format!("unparseable time value: {time_raw}"))?;
        let parse_f64 = |idx: usize| -> Result<f64, String> {
            record.get(idx).unwrap_or_default().trim().parse::<f64>()
                .map_err(|e| format!("bad numeric value at column {idx}: {e}"))
        };
        bars.push(Bar::new(
            time_ms,
            symbol,
            parse_f64(open_idx)?,
            parse_f64(high_idx)?,
            parse_f64(low_idx)?,
            parse_f64(close_idx)?,
            vol_idx.map(parse_f64).transpose()?.unwrap_or(0.0),
        ));
    }
    Ok(bars)
}

/// Accepts a Unix timestamp (seconds or milliseconds) or an RFC3339/`YYYY-MM-DD` date string.
/// Returns **milliseconds**.
fn parse_time_ms(raw: &str) -> Option<i64> {
    let raw = raw.trim();
    if let Ok(n) = raw.parse::<i64>() {
        // Anything with more than 10 digits (post-2001 in seconds) is almost certainly milliseconds.
        return Some(if n > 9_999_999_999 { n } else { n * 1000 });
    }
    if let Ok(dt) = chrono::DateTime::parse_from_rfc3339(raw) {
        return Some(dt.timestamp_millis());
    }
    if let Ok(date) = chrono::NaiveDate::parse_from_str(raw, "%Y-%m-%d") {
        return Some(date.and_hms_opt(0, 0, 0)?.and_utc().timestamp_millis());
    }
    None
}
