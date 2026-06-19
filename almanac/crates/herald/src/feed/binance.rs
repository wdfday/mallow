//! Binance WebSocket kline feed — multi-timeframe.
//!
//! Uses the combined-stream endpoint so all symbols × TFs share a single
//! TCP connection. Each kline update is forwarded as a [`BarEvent`]:
//! - `closed = false` → forming bar (updated on every trade tick)
//! - `closed = true`  → confirmed bar (emitted once at candle close)

use std::sync::Arc;
use std::time::Duration;

use alm_core::Timeframe;
use alm_ledger::Ledger;
use futures::{SinkExt, StreamExt};
use tokio::time::timeout;
use metrics::{counter, gauge};
use serde::Deserialize;
use tokio_tungstenite::{connect_async, tungstenite::Message};
use tracing::{debug, info, warn};

use super::{BarEvent, BarTx};
use super::rest::{gap_fill_symbol, Exchange};

const WS_BASE: &str = "wss://stream.binance.com:9443/stream";
const RECONNECT: Duration = Duration::from_secs(5);
/// Max silence before we assume a TCP half-open and force reconnect.
/// Binance sends a server ping ~every 3 min; 90 s is a safe margin.
const READ_TIMEOUT: Duration = Duration::from_secs(90);
/// Max concurrent REST requests during reconnect gap-fill.
/// Binance REST rate limit: 1 200 weight/min on the `/klines` endpoint
/// (weight=1 each). 4 concurrent is conservative and avoids 429s.
const GAP_FILL_CONCURRENCY: usize = 4;

/// Spawn a task that streams klines for `symbol_tfs` on a single combined-stream connection.
///
/// `symbol_tfs`: per-symbol `(raw_symbol, Vec<Timeframe>)` from [`SymbolConfig::binance_symbol_tfs`].
/// Each symbol subscribes only its declared TFs; unknown TF strings were already filtered at parse
/// time. All symbols × TFs share one TCP connection (Binance limit: 1 024 streams per connection).
///
/// Reconnects automatically on disconnect; runs a REST gap-fill before resuming the WS loop.
/// Task exits when `tx` is dropped (receiver gone).
pub fn spawn(symbol_tfs: Vec<(String, Vec<Timeframe>)>, tx: BarTx, ledger: Arc<Ledger>, tf: Timeframe) {
    if symbol_tfs.is_empty() {
        return;
    }
    tokio::spawn(async move {
        // Build combined-stream URL once — it never changes across reconnects.
        let streams: Vec<String> = symbol_tfs
            .iter()
            .flat_map(|(sym, tfs)| {
                let sym = sym.to_lowercase();
                tfs.iter().filter_map(move |&t| {
                    tf_to_interval(t).map(|iv| format!("{}@kline_{}", sym, iv))
                })
            })
            .collect();
        let url = format!("{}?streams={}", WS_BASE, streams.join("/"));
        let total_tfs: usize = symbol_tfs.iter().map(|(_, tfs)| tfs.len()).sum();
        info!(
            symbols = symbol_tfs.len(),
            total_streams = streams.len(),
            avg_tfs_per_symbol = total_tfs.checked_div(symbol_tfs.len()).unwrap_or(0),
            "binance: subscribing to combined stream"
        );

        let mut reconnect_n: u32 = 0;
        loop {
            if reconnect_n > 0 {
                counter!("herald_feed_reconnects_total", "source" => "binance").increment(1);
                info!(symbols = symbol_tfs.len(), reconnect_n, "binance: reconnect gap-fill");
                let sem = Arc::new(tokio::sync::Semaphore::new(GAP_FILL_CONCURRENCY));
                let tasks: Vec<_> = symbol_tfs.iter().map(|(sym, tfs)| {
                    let live_sym = format!("binance:{}", sym.to_uppercase());
                    let last_ts = ledger.with_state(&live_sym, tf, |s| s.last_ts).flatten();
                    let base_from_ms = last_ts.map(|ts| ts + tf.duration_ms());
                    let ledger = ledger.clone();
                    let sym = sym.clone();
                    let tfs = tfs.clone();
                    let sem = sem.clone();
                    async move {
                        let _permit = sem.acquire_owned().await.expect("semaphore closed");
                        gap_fill_symbol(&ledger, tf, &format!("binance:{}", sym.to_uppercase()), &sym, Exchange::Binance, base_from_ms, &tfs).await;
                    }
                }).collect();
                futures::future::join_all(tasks).await;
            }
            match run_once(&url, &tx).await {
                Ok(()) => {
                    gauge!("herald_feed_ws_connected", "source" => "binance").set(0.0);
                    info!(symbols = symbol_tfs.len(), reconnect_n, "binance: feed task exiting (bar channel closed)");
                    return;
                }
                Err(e) => {
                    gauge!("herald_feed_ws_connected", "source" => "binance").set(0.0);
                    if tx.is_closed() {
                        info!(symbols = symbol_tfs.len(), reconnect_n, "binance: feed task exiting (bar channel closed after error)");
                        return;
                    }
                    reconnect_n += 1;
                    warn!(err = %e, reconnect_n, wait_secs = RECONNECT.as_secs(), "binance: disconnected, reconnecting");
                    tokio::time::sleep(RECONNECT).await;
                }
            }
        }
    });
}

async fn run_once(url: &str, tx: &BarTx) -> anyhow::Result<()> {
    let (ws, _) = connect_async(url).await?;
    info!("binance: connected");
    gauge!("herald_feed_ws_connected", "source" => "binance").set(1.0);
    let (mut write, mut read) = ws.split();

    loop {
        let msg = match timeout(READ_TIMEOUT, read.next()).await {
            Ok(Some(Ok(m))) => m,
            Ok(Some(Err(e))) => {
                if tx.is_closed() {
                    let _ = write.close().await;
                    return Ok(());
                }
                return Err(e.into());
            }
            Ok(None) => return Err(anyhow::anyhow!("stream ended")),
            Err(_elapsed) => {
                warn!("binance: read timeout ({READ_TIMEOUT:?}) — reconnecting");
                return Err(anyhow::anyhow!("read timeout"));
            }
        };

        let text = match msg {
            Message::Text(t) => t,
            Message::Ping(d) => {
                let _ = write.send(Message::Pong(d)).await;
                continue;
            }
            Message::Close(_) => return Err(anyhow::anyhow!("server closed")),
            _ => continue,
        };

        // Capture arrival time before JSON parse so it's as close to
        // wire receipt as possible.
        let received_at_ms = super::now_ms();

        let Ok(env) = serde_json::from_str::<Envelope>(&text) else {
            continue;
        };
        let k = &env.data.k;

        let Some(tf) = interval_to_tf(&k.interval) else {
            continue;
        };

        let bar = alm_core::Bar::new(
            k.open_time,
            &format!("binance:{}", env.data.symbol.to_uppercase()),
            k.open,
            k.high,
            k.low,
            k.close,
            k.volume,
        );
        if k.closed {
            info!(symbol = %bar.symbol, ?tf, close = bar.close, "binance: bar closed");
            let sym = env.data.symbol.to_uppercase();
            counter!("herald_feed_bars_total", "source" => "binance", "symbol" => sym).increment(1);
        }

        if tx.send(BarEvent { tf, bar, closed: k.closed, received_at_ms }).await.is_err() {
            let _ = write.close().await;
            return Ok(());
        }
    }
}

// ── Interval ↔ Timeframe ──────────────────────────────────────────────────────

fn tf_to_interval(tf: Timeframe) -> Option<&'static str> {
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

fn interval_to_tf(iv: &str) -> Option<Timeframe> {
    match iv {
        "1m"  => Some(Timeframe::M1),
        "3m"  => Some(Timeframe::M3),
        "5m"  => Some(Timeframe::M5),
        "15m" => Some(Timeframe::M15),
        "30m" => Some(Timeframe::M30),
        "1h"  => Some(Timeframe::H1),
        "2h"  => Some(Timeframe::H2),
        "4h"  => Some(Timeframe::H4),
        "6h"  => Some(Timeframe::H6),
        "12h" => Some(Timeframe::H12),
        "1d"  => Some(Timeframe::D1),
        "1w"  => Some(Timeframe::W1),
        "1M"  => Some(Timeframe::MN),
        _     => None,
    }
}

// ── JSON deserialization ──────────────────────────────────────────────────────

#[derive(Deserialize)]
struct Envelope {
    data: KlineMsg,
}

#[derive(Deserialize)]
struct KlineMsg {
    #[serde(rename = "s")]
    symbol: String,
    #[serde(rename = "k")]
    k: Kline,
}

#[derive(Deserialize)]
struct Kline {
    #[serde(rename = "t")]
    open_time: i64,
    #[serde(rename = "T")]
    _close_time: i64,
    /// Interval string, e.g. `"1m"`, `"1h"`.
    #[serde(rename = "i")]
    interval: String,
    #[serde(rename = "o", deserialize_with = "de_f64")]
    open: f64,
    #[serde(rename = "h", deserialize_with = "de_f64")]
    high: f64,
    #[serde(rename = "l", deserialize_with = "de_f64")]
    low: f64,
    #[serde(rename = "c", deserialize_with = "de_f64")]
    close: f64,
    #[serde(rename = "v", deserialize_with = "de_f64")]
    volume: f64,
    /// `true` when the candle is closed/confirmed.
    #[serde(rename = "x")]
    closed: bool,
}

fn de_f64<'de, D: serde::Deserializer<'de>>(d: D) -> Result<f64, D::Error> {
    use serde::de::Error;
    #[derive(Deserialize)]
    #[serde(untagged)]
    enum NumOrStr { Num(f64), Str(String) }
    match NumOrStr::deserialize(d)? {
        NumOrStr::Num(n) => Ok(n),
        NumOrStr::Str(s) => s.parse().map_err(D::Error::custom),
    }
}
