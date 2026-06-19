//! REST gap-fill: fetch historical bars from Binance/OKX to prime the ledger
//! before the live WebSocket feed takes over.
//!
//! Strategy:
//! - Base TF (M1): bridge the gap from the last parquet bar to now.
//! - Every other subscribed TF: independently fetch the last `WINDOW_BARS`
//!   closed bars so each ledger slice starts warm without a resampler.

use std::time::Duration;

use alm_core::{Bar, Timeframe};
use anyhow::{Context, Result};
use serde::Deserialize;
use tokio::time::sleep;
use tracing::{debug, info, warn};
use metrics::{counter, histogram};

/// Minimum gap between OKX REST pages (history-candles limit: 20 req/2s).
/// 250 ms → max 4 req/s, well under the quota.
const OKX_PAGE_DELAY: Duration = Duration::from_millis(250);
/// Back-off delay on 429 (doubles each retry up to MAX).
const OKX_RETRY_DELAY: Duration = Duration::from_secs(2);
/// Maximum 429 retries per page before giving up.
const OKX_MAX_RETRIES: u32 = 8;

/// Bars per Binance REST page (max 1 000).
const BINANCE_PAGE: i64 = 1_000;
/// Bars per OKX history-candles page (API hard cap 300).
const OKX_PAGE: i64 = 300;
/// Bars fetched per TF on startup (fills the default ledger window).
const WINDOW_BARS: i64 = 1_000;
/// Maximum time to wait for a single REST fetch during gap-fill.
const GAP_FILL_TIMEOUT: Duration = Duration::from_secs(60);

// ── Binance ───────────────────────────────────────────────────────────────────

const BINANCE_KLINES: &str = "https://api.binance.com/api/v3/klines";

fn binance_interval(tf: Timeframe) -> Option<&'static str> {
    match tf {
        Timeframe::M1  => Some("1m"),
        Timeframe::M3  => Some("3m"),
        Timeframe::M5  => Some("5m"),
        Timeframe::M15 => Some("15m"),
        Timeframe::M30 => Some("30m"),
        Timeframe::H1  => Some("1h"),
        Timeframe::H2  => Some("2h"),
        Timeframe::H4  => Some("4h"),
        Timeframe::H6  => Some("6h"),
        Timeframe::H12 => Some("12h"),
        Timeframe::D1  => Some("1d"),
        Timeframe::W1  => Some("1w"),
        Timeframe::MN  => Some("1M"),
        _ => None,
    }
}

/// Fetch closed bars for `symbol` (Binance format, e.g. "BTCUSDT") at `tf`
/// in the half-open interval `[from_ms, to_ms)`. Bars are returned ascending.
pub async fn fetch_binance(symbol: &str, tf: Timeframe, from_ms: i64, to_ms: i64) -> Result<Vec<Bar>> {
    if from_ms >= to_ms {
        return Ok(vec![]);
    }
    let interval = binance_interval(tf)
        .with_context(|| format!("no Binance interval for {tf:?}"))?;
    let tf_ms = tf.duration_ms();
    let client = reqwest::Client::new();
    let mut bars = Vec::new();
    let mut cursor = from_ms;

    loop {
        let end = (cursor + BINANCE_PAGE * tf_ms).min(to_ms);
        let rows: Vec<Vec<serde_json::Value>> = client
            .get(BINANCE_KLINES)
            .query(&[
                ("symbol", symbol),
                ("interval", interval),
                ("startTime", &cursor.to_string()),
                ("endTime", &(end - 1).to_string()),
                ("limit", &BINANCE_PAGE.to_string()),
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
        debug!(symbol, ?tf, page_start = cursor, bars = fetched, "binance REST page");

        let mut last_page_ts: Option<i64> = bars.last().map(|b: &Bar| b.timestamp);
        for row in rows {
            if row.len() < 6 { continue; }
            let ts = row[0].as_i64().unwrap_or(0);
            if ts >= to_ms { break; }
            if let Some(prev) = last_page_ts {
                let gap = ts - prev;
                if gap > tf_ms * 2 {
                    warn!(
                        symbol, ?tf,
                        prev_ts = prev, bar_ts = ts,
                        gap_ms = gap,
                        expected_ms = tf_ms,
                        "binance REST: gap inside page response",
                    );
                }
            }
            last_page_ts = Some(ts);
            bars.push(Bar::new(
                ts, symbol,
                val_f64(&row[1]), val_f64(&row[2]),
                val_f64(&row[3]), val_f64(&row[4]),
                val_f64(&row[5]),
            ));
        }

        if fetched < BINANCE_PAGE as usize || end >= to_ms { break; }
        cursor = end;
    }

    info!(symbol, ?tf, bars = bars.len(), "binance REST fetch done");
    Ok(bars)
}

// ── OKX ──────────────────────────────────────────────────────────────────────

const OKX_CANDLES: &str = "https://www.okx.com/api/v5/market/history-candles";

fn okx_bar(tf: Timeframe) -> Option<&'static str> {
    match tf {
        Timeframe::M1  => Some("1m"),
        Timeframe::M3  => Some("3m"),
        Timeframe::M5  => Some("5m"),
        Timeframe::M15 => Some("15m"),
        Timeframe::M30 => Some("30m"),
        Timeframe::H1  => Some("1H"),
        Timeframe::H2  => Some("2H"),
        Timeframe::H4  => Some("4H"),
        Timeframe::H6  => Some("6H"),
        Timeframe::H12 => Some("12H"),
        Timeframe::D1  => Some("1D"),
        Timeframe::W1  => Some("1W"),
        Timeframe::MN  => Some("1M"),
        _ => None,
    }
}

/// Fetch closed bars for `symbol` (OKX format, e.g. "BTC-USDT") at `tf`
/// in `[from_ms, to_ms)`. OKX returns newest-first; we reverse before returning.
///
/// OKX pagination: `after=X` means "return bars with ts < X" (older data).
/// We start at `cursor = to_ms` and walk backward one page at a time.
pub async fn fetch_okx(symbol: &str, tf: Timeframe, from_ms: i64, to_ms: i64) -> Result<Vec<Bar>> {
    if from_ms >= to_ms {
        return Ok(vec![]);
    }
    let bar_param = okx_bar(tf)
        .with_context(|| format!("no OKX bar param for {tf:?}"))?;
    let client = reqwest::Client::new();
    let mut bars: Vec<Bar> = Vec::new();
    let mut cursor = to_ms;

    let mut retry_count = 0u32;
    loop {
        let http_resp = client
            .get(OKX_CANDLES)
            .query(&[
                ("instId", symbol),
                ("bar", bar_param),
                ("after", &cursor.to_string()),
                ("limit", &OKX_PAGE.to_string()),
            ])
            .send()
            .await
            .with_context(|| format!("okx REST request failed for {symbol}"))?;

        let status = http_resp.status();
        if status == reqwest::StatusCode::TOO_MANY_REQUESTS {
            retry_count += 1;
            if retry_count > OKX_MAX_RETRIES {
                let body = http_resp.text().await.unwrap_or_default();
                anyhow::bail!("okx REST 429 for {symbol} after {OKX_MAX_RETRIES} retries: {body}");
            }
            let delay = OKX_RETRY_DELAY * (1u32 << (retry_count - 1).min(5));
            warn!(symbol, ?tf, %bar_param, retry = retry_count, ?delay, "okx REST 429 — backing off");
            sleep(delay).await;
            continue; // retry same cursor
        }
        retry_count = 0; // reset on success
        if !status.is_success() {
            let body = http_resp.text().await.unwrap_or_default();
            anyhow::bail!("okx REST {status} for {symbol} bar={bar_param}: {body}");
        }

        let resp: OkxResp = http_resp
            .json()
            .await
            .with_context(|| format!("okx REST JSON parse failed for {symbol}"))?;

        let fetched = resp.data.len();
        debug!(symbol, ?tf, cursor, bars = fetched, "okx REST page");

        if fetched == 0 { break; }

        let mut oldest_ts = cursor;
        for row in resp.data {
            if row.len() < 6 { continue; }
            let ts: i64 = row[0].parse().unwrap_or(0);
            if ts < oldest_ts { oldest_ts = ts; }
            if ts < from_ms || ts >= to_ms { continue; }
            if row.get(8).map(|s| s.as_str()) != Some("1") { continue; }
            bars.push(Bar::new(
                ts, symbol,
                parse_f64(&row[1]), parse_f64(&row[2]),
                parse_f64(&row[3]), parse_f64(&row[4]),
                parse_f64(&row[5]),
            ));
        }

        if fetched < OKX_PAGE as usize || oldest_ts <= from_ms || oldest_ts >= cursor { break; }
        cursor = oldest_ts;

        // Throttle between pages to stay under OKX rate limit (20 req/2s).
        sleep(OKX_PAGE_DELAY).await;
    }

    bars.sort_unstable_by_key(|b| b.timestamp);
    bars.dedup_by_key(|b| b.timestamp);
    info!(symbol, ?tf, bars = bars.len(), "okx REST fetch done");
    Ok(bars)
}

// ── Gap-fill orchestration ────────────────────────────────────────────────────

/// Source exchange for a symbol.
pub enum Exchange {
    Binance,
    Okx,
}

/// Prime the ledger for `live_sym` across all `subscribe_tfs`:
///
/// - **Base TF**: if `base_from_ms` is `Some(t)`, fetches the gap `[t, now)`
///   to bridge from the last parquet bar to the present.
/// - **Every other subscribed TF**: fetches the last `WINDOW_BARS` closed bars
///   independently. Each TF self-sources its own history — no resampler.
///
/// None of these bars trigger observer fan-out (observers are subscribed after
/// gap-fill completes).
pub async fn gap_fill_symbol(
    ledger: &alm_ledger::Ledger,
    base_tf: Timeframe,
    live_sym: &str,
    rest_sym: &str,
    exchange: Exchange,
    base_from_ms: Option<i64>,
    subscribe_tfs: &[Timeframe],
) {
    let gap_fill_start = std::time::Instant::now();
    let now_ms = chrono::Utc::now().timestamp_millis();

    // 1. Base TF: bridge the gap from the last parquet bar to now, or — when no
    //    parquet exists — seed the ledger with the last WINDOW_BARS bars.
    {
        let tf_ms = base_tf.duration_ms();
        let to_ms = (now_ms / tf_ms) * tf_ms;
        let from_ms = match base_from_ms {
            Some(t) => t,
            None => to_ms - WINDOW_BARS * tf_ms,
        };
        if from_ms < to_ms {
            let label = if base_from_ms.is_some() { "parquet gap" } else { "cold seed" };
            info!(
                symbol = live_sym, ?base_tf,
                expected_bars = (to_ms - from_ms) / tf_ms,
                "gap-fill: base TF {label}"
            );
            let result = tokio::time::timeout(
                GAP_FILL_TIMEOUT,
                fetch_tf(&exchange, rest_sym, base_tf, from_ms, to_ms),
            ).await.unwrap_or_else(|_| {
                warn!(symbol = live_sym, ?base_tf, timeout_secs = 60u64, "gap-fill: REST fetch timed out — skipping");
                counter!("herald_gap_fill_timeouts_total", "symbol" => live_sym.to_string()).increment(1);
                Err(anyhow::anyhow!("gap-fill fetch timeout after 60s"))
            });
            advance_bars(ledger, result, live_sym, base_tf, label);
        }
    }

    // 2. Every other subscribed TF: fetch last WINDOW_BARS bars.
    for &tf in subscribe_tfs {
        if tf == base_tf { continue; }
        let tf_ms = tf.duration_ms();
        let to_ms  = (now_ms / tf_ms) * tf_ms;
        let from_ms = to_ms - WINDOW_BARS * tf_ms;
        info!(symbol = live_sym, ?tf, bars = WINDOW_BARS, "gap-fill: fetching HTF window");
        let result = tokio::time::timeout(
            GAP_FILL_TIMEOUT,
            fetch_tf(&exchange, rest_sym, tf, from_ms, to_ms),
        ).await.unwrap_or_else(|_| {
            warn!(symbol = live_sym, ?tf, timeout_secs = 60u64, "gap-fill: REST fetch timed out — skipping");
            counter!("herald_gap_fill_timeouts_total", "symbol" => live_sym.to_string()).increment(1);
            Err(anyhow::anyhow!("gap-fill fetch timeout after 60s"))
        });
        advance_bars(ledger, result, live_sym, tf, "HTF window");
    }
    histogram!("herald_gap_fill_duration_ms",
        "symbol" => live_sym.to_string()
    ).record(gap_fill_start.elapsed().as_millis() as f64);
}

async fn fetch_tf(
    exchange: &Exchange,
    rest_sym: &str,
    tf: Timeframe,
    from_ms: i64,
    to_ms: i64,
) -> Result<Vec<Bar>> {
    match exchange {
        Exchange::Binance => fetch_binance(rest_sym, tf, from_ms, to_ms).await,
        Exchange::Okx => fetch_okx(rest_sym, tf, from_ms, to_ms).await,
    }
}

fn advance_bars(
    ledger: &alm_ledger::Ledger,
    result: Result<Vec<Bar>>,
    live_sym: &str,
    tf: Timeframe,
    label: &str,
) {
    match result {
        Ok(bars) => {
            let count = bars.len();
            counter!("herald_gap_fill_bars_total",
                "symbol" => live_sym.to_string(),
                "tf"     => format!("{:?}", tf),
            ).increment(count as u64);
            let tf_ms = tf.duration_ms();
            let mut gap_count = 0usize;
            for w in bars.windows(2) {
                let gap_ms = w[1].timestamp - w[0].timestamp;
                if gap_ms > tf_ms * 2 {
                    gap_count += 1;
                    warn!(
                        symbol = live_sym, ?tf, label,
                        bar0_ts = w[0].timestamp,
                        bar1_ts = w[1].timestamp,
                        gap_ms,
                        expected_ms = tf_ms,
                        "REST fetch: gap between consecutive bars",
                    );
                }
            }
            if gap_count > 0 {
                warn!(
                    symbol = live_sym, ?tf, label,
                    gap_count, total_bars = count,
                    "REST fetch: gaps found in fetched bars",
                );
            }
            for mut bar in bars {
                bar.symbol = live_sym.to_string();
                let _ = ledger.advance(tf, bar);
            }
            info!(symbol = live_sym, ?tf, bars = count, "gap-fill: {} advanced", label);
        }
        Err(e) => {
            warn!(symbol = live_sym, ?tf, err = %e, "gap-fill: {} failed — continuing", label);
        }
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

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
