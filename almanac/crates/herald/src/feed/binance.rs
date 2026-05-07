//! Binance WebSocket kline feed — multi-timeframe.
//!
//! Uses the combined-stream endpoint so all symbols × TFs share a single
//! TCP connection. Each kline update is forwarded as a [`BarEvent`]:
//! - `closed = false` → forming bar (updated on every trade tick)
//! - `closed = true`  → confirmed bar (emitted once at candle close)

use std::time::Duration;

use alm_core::Timeframe;
use futures::{SinkExt, StreamExt};
use serde::Deserialize;
use tokio_tungstenite::{connect_async, tungstenite::Message};
use tracing::{debug, info, warn};

use super::{BarEvent, BarTx, SUBSCRIBE_TFS};

const WS_BASE: &str = "wss://stream.binance.com:9443/stream";
const RECONNECT: Duration = Duration::from_secs(5);

/// Spawn a task that streams klines for `symbols` across all [`SUBSCRIBE_TFS`].
/// Sends [`BarEvent`]s to `tx`. Reconnects automatically on disconnect.
/// Task exits when `tx` is dropped (receiver gone).
pub fn spawn(symbols: Vec<String>, tx: BarTx) {
    if symbols.is_empty() {
        return;
    }
    tokio::spawn(async move {
        let streams: Vec<String> = symbols
            .iter()
            .flat_map(|s| {
                let sym = s.to_lowercase();
                SUBSCRIBE_TFS.iter().filter_map(move |&tf| {
                    tf_to_interval(tf).map(|iv| format!("{}@kline_{}", sym, iv))
                })
            })
            .collect();
        let url = format!("{}?streams={}", WS_BASE, streams.join("/"));
        info!(
            symbols = symbols.len(),
            tfs = SUBSCRIBE_TFS.len(),
            streams = streams.len(),
            "binance: subscribing to combined stream"
        );

        loop {
            match run_once(&url, &tx).await {
                Ok(()) => return,
                Err(e) => {
                    if tx.is_closed() {
                        return;
                    }
                    warn!(err = %e, wait_secs = RECONNECT.as_secs(), "binance: disconnected, reconnecting");
                    tokio::time::sleep(RECONNECT).await;
                }
            }
        }
    });
}

async fn run_once(url: &str, tx: &BarTx) -> anyhow::Result<()> {
    let (ws, _) = connect_async(url).await?;
    info!("binance: connected");
    let (mut write, mut read) = ws.split();

    while let Some(msg) = read.next().await {
        let msg = match msg {
            Ok(m) => m,
            Err(e) => {
                if tx.is_closed() {
                    let _ = write.close().await;
                    return Ok(());
                }
                return Err(e.into());
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

        let Ok(env) = serde_json::from_str::<Envelope>(&text) else {
            continue;
        };
        let k = &env.data.k;

        let Some(tf) = interval_to_tf(&k.interval) else {
            continue;
        };

        let bar = alm_core::Bar::new(
            k.open_time,
            &env.data.symbol.to_uppercase(),
            k.open,
            k.high,
            k.low,
            k.close,
            k.volume,
        );
        if k.closed {
            debug!(symbol = %bar.symbol, ?tf, close = bar.close, "binance: bar closed");
        }

        if tx.send(BarEvent { tf, bar, closed: k.closed }).is_err() {
            let _ = write.close().await;
            return Ok(());
        }
    }
    Err(anyhow::anyhow!("stream ended"))
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
        Timeframe::H8  => Some("8h"),
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
        "8h"  => Some(Timeframe::H8),
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
