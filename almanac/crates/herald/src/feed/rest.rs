//! REST gap-fill: fetch closed M1 klines from Binance/OKX to cover the gap
//! between the last parquet bar and the current time, before the live
//! WebSocket feed takes over.

use alm_core::{Bar, Timeframe};
use anyhow::{Context, Result};
use serde::Deserialize;
use tracing::{debug, info, warn};

use crate::resampler::SymbolResamplers;

/// Maximum bars per REST request for both exchanges.
const PAGE: i64 = 1_000;
/// 1-minute in milliseconds.
const M1_MS: i64 = 60_000;

// ── Binance ────────────────────────────────────────────────────────────────────

const BINANCE_KLINES: &str = "https://api.binance.com/api/v3/klines";

/// Fetch all closed M1 bars for `symbol` (Binance format, e.g. "BTCUSDT")
/// in the half-open interval `[from_ms, to_ms)`.
/// Returns bars sorted ascending by open time.
pub async fn fetch_binance(symbol: &str, from_ms: i64, to_ms: i64) -> Result<Vec<Bar>> {
    if from_ms >= to_ms {
        return Ok(vec![]);
    }
    let client = reqwest::Client::new();
    let mut bars = Vec::new();
    let mut cursor = from_ms;

    loop {
        let end = (cursor + PAGE * M1_MS).min(to_ms);
        // Each kline is a JSON array: [openTime, open, high, low, close, vol, ...]
        let rows: Vec<Vec<serde_json::Value>> = client
            .get(BINANCE_KLINES)
            .query(&[
                ("symbol", symbol),
                ("interval", "1m"),
                ("startTime", &cursor.to_string()),
                ("endTime", &(end - 1).to_string()), // exclusive upper bound
                ("limit", &PAGE.to_string()),
            ])
            .send()
            .await
            .with_context(|| format!("binance REST request failed for {symbol}"))?
            .error_for_status()
            .with_context(|| format!("binance REST error status for {symbol}"))?
            .json()
            .await
            .with_context(|| format!("binance REST JSON parse failed for {symbol}"))?;

        let fetched = rows.len();
        debug!(symbol, page_start = cursor, bars = fetched, "binance REST page");

        for row in rows {
            if row.len() < 6 {
                continue;
            }
            let ts = row[0].as_i64().unwrap_or(0);
            // Only include bars whose open time is strictly before to_ms.
            if ts >= to_ms {
                break;
            }
            bars.push(Bar::new(
                ts,
                symbol,
                val_f64(&row[1]),
                val_f64(&row[2]),
                val_f64(&row[3]),
                val_f64(&row[4]),
                val_f64(&row[5]),
            ));
        }

        if fetched < PAGE as usize || end >= to_ms {
            break;
        }
        cursor = end;
    }

    info!(symbol, bars = bars.len(), "binance REST gap-fill done");
    Ok(bars)
}

// ── OKX ───────────────────────────────────────────────────────────────────────

const OKX_CANDLES: &str = "https://www.okx.com/api/v5/market/history-candles";

/// Fetch all closed M1 bars for `symbol` (OKX format, e.g. "BTC-USDT")
/// in the half-open interval `[from_ms, to_ms)`.
/// OKX returns bars in **descending** order; we reverse before returning.
pub async fn fetch_okx(symbol: &str, from_ms: i64, to_ms: i64) -> Result<Vec<Bar>> {
    if from_ms >= to_ms {
        return Ok(vec![]);
    }
    let client = reqwest::Client::new();
    let mut bars: Vec<Bar> = Vec::new();
    // OKX pagination: `before` is exclusive upper bound (ts of the *oldest* bar
    // in the previous page). Start at to_ms, page backwards.
    let mut before = to_ms;

    loop {
        let resp: OkxResp = client
            .get(OKX_CANDLES)
            .query(&[
                ("instId", symbol),
                ("bar", "1m"),
                ("before", &(before - 1).to_string()), // exclusive — bar.ts < before
                ("after", &(from_ms - 1).to_string()),  // exclusive — bar.ts > after
                ("limit", &PAGE.to_string()),
            ])
            .send()
            .await
            .with_context(|| format!("okx REST request failed for {symbol}"))?
            .error_for_status()
            .with_context(|| format!("okx REST error status for {symbol}"))?
            .json()
            .await
            .with_context(|| format!("okx REST JSON parse failed for {symbol}"))?;

        let fetched = resp.data.len();
        debug!(symbol, before, bars = fetched, "okx REST page");

        let mut oldest_ts = before;
        for row in resp.data {
            if row.len() < 6 {
                continue;
            }
            let ts: i64 = row[0].parse().unwrap_or(0);
            if ts < from_ms || ts >= to_ms {
                continue;
            }
            // row[8] = "1" means candle is confirmed/closed.
            if row.get(8).map(|s| s.as_str()) != Some("1") {
                continue;
            }
            if ts < oldest_ts {
                oldest_ts = ts;
            }
            bars.push(Bar::new(
                ts,
                symbol,
                parse_f64(&row[1]),
                parse_f64(&row[2]),
                parse_f64(&row[3]),
                parse_f64(&row[4]),
                parse_f64(&row[5]),
            ));
        }

        if fetched < PAGE as usize || oldest_ts <= from_ms {
            break;
        }
        before = oldest_ts; // page backwards
    }

    // OKX data arrives newest-first; reverse to get chronological order.
    bars.sort_unstable_by_key(|b| b.timestamp);
    bars.dedup_by_key(|b| b.timestamp);

    info!(symbol, bars = bars.len(), "okx REST gap-fill done");
    Ok(bars)
}

// ── Gap-fill orchestration ─────────────────────────────────────────────────────

/// Source exchange for a symbol.
pub enum Exchange {
    Binance,
    Okx,
}

/// Fill the gap `[from_ms, now)` for a single symbol and advance all fetched
/// bars into the ledger **without** triggering observer fan-out (observers are
/// subscribed after gap-fill completes).
///
/// `live_sym` is the symbol name used in the ledger/live feed (e.g. "BTC-USDT"
/// for OKX, "BTCUSDT" for Binance). `rest_sym` is the REST API symbol name
/// (same for Binance; with dashes for OKX).
///
/// `resamplers` is updated in-place so HTF buckets are continuous across
/// the bootstrap → gap-fill → live boundary.
pub async fn gap_fill_symbol(
    ledger: &alm_ledger::Ledger,
    resamplers: &mut SymbolResamplers,
    base_tf: Timeframe,
    live_sym: &str,
    rest_sym: &str,
    exchange: Exchange,
    from_ms: i64,
) {
    let now_ms = chrono::Utc::now().timestamp_millis();
    // Trim to last closed minute to avoid a partial bar.
    let to_ms = (now_ms / M1_MS) * M1_MS;

    if from_ms >= to_ms {
        debug!(symbol = live_sym, "gap-fill: no gap to fill");
        return;
    }

    let expected = (to_ms - from_ms) / M1_MS;
    info!(
        symbol = live_sym,
        from_ms,
        to_ms,
        expected_bars = expected,
        "gap-fill: fetching"
    );

    let result = match exchange {
        Exchange::Binance => fetch_binance(rest_sym, from_ms, to_ms).await,
        Exchange::Okx => fetch_okx(rest_sym, from_ms, to_ms).await,
    };

    match result {
        Ok(bars) => {
            let mut count = 0usize;
            for mut bar in bars {
                bar.symbol = live_sym.to_string();
                let _ = ledger.advance(base_tf, bar.clone());
                for (htf, htf_bar) in resamplers.push(&bar) {
                    let _ = ledger.advance(htf, htf_bar);
                }
                count += 1;
            }
            info!(symbol = live_sym, bars = count, "gap-fill: advanced into ledger");
        }
        Err(e) => {
            warn!(symbol = live_sym, err = %e, "gap-fill: REST fetch failed — continuing without gap data");
        }
    }
}

// ── Helpers ────────────────────────────────────────────────────────────────────

/// Parse a `serde_json::Value` that is either a JSON number or a quoted
/// numeric string (Binance returns OHLCV as strings in some endpoints).
fn val_f64(v: &serde_json::Value) -> f64 {
    match v {
        serde_json::Value::Number(n) => n.as_f64().unwrap_or(0.0),
        serde_json::Value::String(s) => s.parse().unwrap_or(0.0),
        _ => 0.0,
    }
}

fn parse_f64(s: &str) -> f64 {
    s.parse().unwrap_or(0.0)
}

#[derive(Deserialize)]
struct OkxResp {
    data: Vec<Vec<String>>,
}
