//! On-demand data loader — the user adds a symbol to their research universe and this
//! module syncs its history into the `~/Fathom/.data` lake in the background.
//!
//! Source: Binance Vision flat-files (`data.binance.vision`) — public CDN, no API key —
//! the same mechanism as `hist-data/internal/provider/binanceflat` (Go), ported here.
//! Monthly M1 ZIPs are downloaded (default lookback 24 complete months, checksum-verified,
//! resumable — months already on disk are skipped) and written as parquet in the layout
//! `BinanceFlat/M1/{SYMBOL}/{SYMBOL}_M1_{YYYY-MM}.parquet`. After the M1 pass a resampled
//! TF ladder (M5…D1) is materialized so both the chart and `alm_engine::backtest::run`
//! find exact-TF files without any read-time resampling.

use std::io::Read;
use std::path::Path;
use std::sync::Arc;

use alm_core::{Bar, Timeframe};
use alm_data::{BarFeed, ParquetFeed};
use serde::Serialize;
use sha2::{Digest, Sha256};
use tauri::{AppHandle, Emitter};

use super::{catalog, home, registry};

const BASE_URL: &str = "https://data.binance.vision";
const PROVIDER_DIR: &str = "BinanceFlat";
const DEFAULT_MONTHS: u32 = 24;
/// Consecutive missing months (scanning newest→oldest) after which we assume the
/// symbol's listing date has been passed and stop.
const MISS_STREAK_STOP: u32 = 3;
const LADDER: [Timeframe; 6] =
    [Timeframe::M5, Timeframe::M15, Timeframe::M30, Timeframe::H1, Timeframe::H4, Timeframe::D1];

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SyncProgress {
    pub symbol: String,
    /// `"m1"` (downloading months) | `"ladder"` (resampling TFs) | `"done"` | `"error"`
    pub stage: String,
    pub detail: Option<String>,
    pub done: u32,
    pub total: u32,
    pub error: Option<String>,
}

fn emit(app: &AppHandle, p: SyncProgress) {
    let _ = app.emit("data://sync-progress", &p);
}

#[tauri::command]
pub async fn symbols_add(app: AppHandle, symbol: String) -> Result<(), String> {
    let sym = symbol.trim().to_uppercase();
    if sym.is_empty() {
        return Err("empty symbol".into());
    }
    registry::upsert_symbol(&sym, "binance")?;
    tauri::async_runtime::spawn(sync_symbol(app, sym));
    Ok(())
}

#[tauri::command]
pub async fn symbols_remove(symbol: String) -> Result<(), String> {
    let sym = symbol.trim().to_uppercase();
    registry::delete_symbol(&sym)?;
    let provider = home::lake_dir()?.join(PROVIDER_DIR);
    if let Ok(tfs) = std::fs::read_dir(&provider) {
        for tf in tfs.flatten() {
            let sym_dir = tf.path().join(&sym);
            if sym_dir.is_dir() {
                let _ = std::fs::remove_dir_all(sym_dir);
            }
        }
    }
    Ok(())
}

async fn sync_symbol(app: AppHandle, sym: String) {
    let _ = registry::set_symbol_status(&sym, "syncing", None);
    match sync_symbol_inner(&app, &sym).await {
        Ok(months) if months > 0 => {
            let _ = registry::set_symbol_status(&sym, "ready", None);
            emit(&app, SyncProgress { symbol: sym, stage: "done".into(), detail: None, done: months, total: months, error: None });
        }
        Ok(_) => {
            let msg = "no data on Binance Vision for this symbol (typo?)".to_string();
            let _ = registry::set_symbol_status(&sym, "error", Some(&msg));
            emit(&app, SyncProgress { symbol: sym, stage: "error".into(), detail: None, done: 0, total: 0, error: Some(msg) });
        }
        Err(e) => {
            let _ = registry::set_symbol_status(&sym, "error", Some(&e));
            emit(&app, SyncProgress { symbol: sym, stage: "error".into(), detail: None, done: 0, total: 0, error: Some(e) });
        }
    }
}

/// Newest→oldest list of the last `n` *complete* months (the current month has no
/// monthly archive yet).
fn last_complete_months(n: u32) -> Vec<(i32, u32)> {
    use chrono::Datelike;
    let today = chrono::Utc::now().date_naive();
    let (mut y, mut m) = (today.year(), today.month());
    let mut out = Vec::with_capacity(n as usize);
    for _ in 0..n {
        if m == 1 {
            y -= 1;
            m = 12;
        } else {
            m -= 1;
        }
        out.push((y, m));
    }
    out
}

async fn sync_symbol_inner(app: &AppHandle, sym: &str) -> Result<u32, String> {
    let m1_dir = home::lake_dir()?.join(PROVIDER_DIR).join("M1").join(sym);
    std::fs::create_dir_all(&m1_dir).map_err(|e| format!("create {}: {e}", m1_dir.display()))?;

    let months = last_complete_months(DEFAULT_MONTHS);
    let total = months.len() as u32;
    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(300))
        .build()
        .map_err(|e| e.to_string())?;

    let mut ok = 0u32;
    let mut miss_streak = 0u32;
    for (i, (year, month)) in months.iter().enumerate() {
        let label = format!("{year:04}-{month:02}");
        emit(app, SyncProgress {
            symbol: sym.to_string(),
            stage: "m1".into(),
            detail: Some(label.clone()),
            done: i as u32,
            total,
            error: None,
        });

        let path = m1_dir.join(format!("{sym}_M1_{label}.parquet"));
        if path.exists() {
            ok += 1;
            miss_streak = 0;
            continue;
        }
        match fetch_month(&client, sym, *year, *month).await? {
            Some(bars) if !bars.is_empty() => {
                write_parquet(&path, &bars)?;
                ok += 1;
                miss_streak = 0;
            }
            _ => {
                miss_streak += 1;
                // Once we have data and then hit a run of missing months going backwards,
                // we've walked past the listing date — nothing older will exist.
                if ok > 0 && miss_streak >= MISS_STREAK_STOP {
                    break;
                }
            }
        }
    }

    if ok > 0 {
        build_ladder(app, sym, &m1_dir)?;
    }
    Ok(ok)
}

/// Downloads + parses one monthly M1 ZIP. `Ok(None)` = 404 (no archive for that month).
async fn fetch_month(
    client: &reqwest::Client,
    sym: &str,
    year: i32,
    month: u32,
) -> Result<Option<Vec<Bar>>, String> {
    let url = format!("{BASE_URL}/data/spot/monthly/klines/{sym}/1m/{sym}-1m-{year:04}-{month:02}.zip");
    let resp = client.get(&url).send().await.map_err(|e| format!("GET {url}: {e}"))?;
    if resp.status() == reqwest::StatusCode::NOT_FOUND {
        return Ok(None);
    }
    if !resp.status().is_success() {
        return Err(format!("HTTP {} for {url}", resp.status()));
    }
    let buf = resp.bytes().await.map_err(|e| format!("read {url}: {e}"))?;

    // Checksum sidecar (SHA256). Missing (404) sidecars are skipped — older files
    // may not have one; a mismatch is a hard error. Same policy as hist-data's client.
    let cs_resp = client
        .get(format!("{url}.CHECKSUM"))
        .send()
        .await
        .map_err(|e| format!("checksum GET: {e}"))?;
    if cs_resp.status().is_success() {
        let body = cs_resp.text().await.map_err(|e| e.to_string())?;
        let want = body.split_whitespace().next().unwrap_or_default().to_lowercase();
        let got = hex::encode(Sha256::digest(&buf));
        if !want.is_empty() && got != want {
            return Err(format!("checksum mismatch for {url}"));
        }
    }

    let cursor = std::io::Cursor::new(buf.as_ref());
    let mut zip = zip::ZipArchive::new(cursor).map_err(|e| format!("zip open {url}: {e}"))?;
    if zip.is_empty() {
        return Err(format!("empty zip: {url}"));
    }
    let mut csv_raw = String::new();
    zip.by_index(0)
        .map_err(|e| format!("zip entry: {e}"))?
        .read_to_string(&mut csv_raw)
        .map_err(|e| format!("zip read: {e}"))?;

    Ok(Some(parse_vision_csv(&csv_raw, sym)?))
}

/// Vision kline CSV: openTime,O,H,L,C,volume,closeTime,… — no header historically, a
/// header row recently; openTime switched ms→μs around 2025-01. Both handled like the
/// Go client (`hist-data/provider/binanceflat`).
fn parse_vision_csv(raw: &str, sym: &str) -> Result<Vec<Bar>, String> {
    let mut bars = Vec::new();
    for (row, line) in raw.lines().enumerate() {
        if line.trim().is_empty() {
            continue;
        }
        let fields: Vec<&str> = line.split(',').collect();
        if fields.len() < 6 {
            continue;
        }
        let ts = match fields[0].parse::<i64>() {
            Ok(t) => t,
            // Header row is only tolerated as the first line.
            Err(_) if row == 0 => continue,
            Err(e) => return Err(format!("csv row {row}: {e}")),
        };
        let ts = if ts > 100_000_000_000_000 { ts / 1000 } else { ts }; // μs → ms
        let f = |i: usize| fields[i].parse::<f64>().map_err(|e| format!("csv row {row} col {i}: {e}"));
        bars.push(Bar::new(ts, sym, f(1)?, f(2)?, f(3)?, f(4)?, f(5)?));
    }
    Ok(bars)
}

/// Materialize the resampled TF ladder from the symbol's full M1 history — one file per
/// TF: `BinanceFlat/{TF}/{SYMBOL}/{SYMBOL}_{TF}.parquet`. Rebuilt each sync (cheap, and
/// it keeps the ladder consistent with whatever months the M1 pass ended up with).
fn build_ladder(app: &AppHandle, sym: &str, m1_dir: &Path) -> Result<(), String> {
    let mut feed = ParquetFeed::load_dir(m1_dir, sym).map_err(|e| format!("load M1 for ladder: {e}"))?;
    let m1: Vec<Bar> = std::iter::from_fn(|| feed.next()).collect();

    let total = LADDER.len() as u32;
    for (i, tf) in LADDER.iter().enumerate() {
        emit(app, SyncProgress {
            symbol: sym.to_string(),
            stage: "ladder".into(),
            detail: Some(tf.to_string()),
            done: i as u32,
            total,
            error: None,
        });
        let bars = catalog::resample(m1.iter().cloned(), *tf);
        let dir = home::lake_dir()?.join(PROVIDER_DIR).join(tf.to_string()).join(sym);
        std::fs::create_dir_all(&dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
        write_parquet(&dir.join(format!("{sym}_{tf}.parquet")), &bars)?;
    }
    Ok(())
}

/// Write bars as parquet with the `t,o,h,l,c,v` schema `alm_data::ParquetFeed` reads.
/// Atomic: writes to a `.tmp` sibling then renames, so a crash mid-write never leaves a
/// half-file the resume check would mistake for a completed month.
pub fn write_parquet(path: &Path, bars: &[Bar]) -> Result<(), String> {
    use arrow_array::{Float64Array, Int64Array, RecordBatch};
    use arrow_schema::{DataType, Field, Schema};
    use parquet::arrow::ArrowWriter;

    let schema = Arc::new(Schema::new(vec![
        Field::new("t", DataType::Int64, false),
        Field::new("o", DataType::Float64, false),
        Field::new("h", DataType::Float64, false),
        Field::new("l", DataType::Float64, false),
        Field::new("c", DataType::Float64, false),
        Field::new("v", DataType::Float64, false),
    ]));
    let batch = RecordBatch::try_new(
        schema.clone(),
        vec![
            Arc::new(Int64Array::from(bars.iter().map(|b| b.timestamp).collect::<Vec<_>>())),
            Arc::new(Float64Array::from(bars.iter().map(|b| b.open).collect::<Vec<_>>())),
            Arc::new(Float64Array::from(bars.iter().map(|b| b.high).collect::<Vec<_>>())),
            Arc::new(Float64Array::from(bars.iter().map(|b| b.low).collect::<Vec<_>>())),
            Arc::new(Float64Array::from(bars.iter().map(|b| b.close).collect::<Vec<_>>())),
            Arc::new(Float64Array::from(bars.iter().map(|b| b.volume).collect::<Vec<_>>())),
        ],
    )
    .map_err(|e| format!("build batch: {e}"))?;

    let tmp = path.with_extension("parquet.tmp");
    let file = std::fs::File::create(&tmp).map_err(|e| format!("create {}: {e}", tmp.display()))?;
    let mut writer = ArrowWriter::try_new(file, schema, None).map_err(|e| format!("parquet writer: {e}"))?;
    writer.write(&batch).map_err(|e| format!("parquet write: {e}"))?;
    writer.close().map_err(|e| format!("parquet close: {e}"))?;
    std::fs::rename(&tmp, path).map_err(|e| format!("rename {}: {e}", path.display()))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::data::catalog;

    /// The lake's whole value rests on this round-trip: what `write_parquet` produces must be
    /// exactly what `alm_data::ParquetFeed` (and therefore the chart, the catalog, and
    /// `alm_engine::backtest::run`) reads back — and the TF ladder must aggregate correctly.
    #[test]
    fn write_read_resample_roundtrip() {
        let dir = std::env::temp_dir().join(format!("fathom-test-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("FOO_M1_2024-01.parquet");

        // 120 one-minute bars from an hour boundary → exactly 2 complete H1 buckets.
        let t0: i64 = 1_704_067_200_000; // 2024-01-01T00:00:00Z
        let m1: Vec<Bar> = (0..120)
            .map(|i| {
                let p = 100.0 + i as f64;
                Bar::new(t0 + i * 60_000, "FOO", p, p + 1.0, p - 1.0, p + 0.5, 10.0)
            })
            .collect();

        write_parquet(&path, &m1).unwrap();
        let mut feed = ParquetFeed::load(&path, "FOO").unwrap();
        let read: Vec<Bar> = std::iter::from_fn(|| feed.next()).collect();
        assert_eq!(read.len(), 120);
        assert_eq!(read[0].timestamp, t0);
        assert_eq!(read[0].open, 100.0);
        assert_eq!(read[119].close, 219.5);

        let h1 = catalog::resample(read, Timeframe::H1);
        assert_eq!(h1.len(), 2);
        assert_eq!(h1[0].timestamp, t0);
        assert_eq!(h1[0].open, 100.0); // first M1 open
        assert_eq!(h1[0].close, 159.5); // 60th M1 close
        assert_eq!(h1[0].high, 160.0); // max high in hour
        assert_eq!(h1[0].volume, 600.0); // 60 × 10
        assert_eq!(h1[1].close, 219.5);

        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn vision_csv_parses_header_and_microseconds() {
        // Header row + one μs-timestamped row (post-2025 format) + one ms row.
        let csv = "open_time,open,high,low,close,volume,close_time,q,v,n,t,i\n\
                   1704067200000000,1.0,2.0,0.5,1.5,10.0,x,,,,,\n\
                   1704067260000,2.0,3.0,1.5,2.5,20.0,x,,,,,\n";
        let bars = parse_vision_csv(csv, "FOO").unwrap();
        assert_eq!(bars.len(), 2);
        assert_eq!(bars[0].timestamp, 1_704_067_200_000);
        assert_eq!(bars[1].timestamp, 1_704_067_260_000);
        assert_eq!(bars[1].close, 2.5);
    }
}
