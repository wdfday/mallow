use std::time::Duration;

use alm_core::Bar;
use futures::{SinkExt, StreamExt};
use serde::Deserialize;
use tokio_tungstenite::{connect_async, tungstenite::Message};
use tracing::{debug, info, warn};

use super::BarTx;

const WS_BASE: &str = "wss://stream.binance.com:9443/stream";
const RECONNECT: Duration = Duration::from_secs(5);

/// Spawn a task that streams closed M1 klines for `symbols` from Binance.
/// Sends completed `Bar`s to `tx`. Reconnects automatically on disconnect.
/// Task exits when `tx` is dropped (receiver gone).
pub fn spawn(symbols: Vec<String>, tx: BarTx) {
    if symbols.is_empty() {
        return;
    }
    tokio::spawn(async move {
        let streams: Vec<String> = symbols
            .iter()
            .map(|s| format!("{}@kline_1m", s.to_lowercase()))
            .collect();
        let url = format!("{}?streams={}", WS_BASE, streams.join("/"));

        loop {
            match run_once(&url, &tx).await {
                Ok(()) => return, // tx dropped — clean shutdown
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
        if !k.closed {
            continue;
        }

        let bar = Bar::new(
            k.open_time,
            &env.data.symbol.to_uppercase(),
            k.open,
            k.high,
            k.low,
            k.close,
            k.volume,
        );
        debug!(symbol = %bar.symbol, close = bar.close, "binance: bar closed");

        if tx.send(bar).is_err() {
            let _ = write.close().await;
            return Ok(());
        }
    }
    Err(anyhow::anyhow!("stream ended"))
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
    // CloseTime must be declared to prevent case-insensitive shadowing of "t".
    #[serde(rename = "T")]
    _close_time: i64,
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
    #[serde(rename = "x")]
    closed: bool,
}

// Binance OHLCV fields may be either a JSON number or a quoted numeric string.
fn de_f64<'de, D: serde::Deserializer<'de>>(d: D) -> Result<f64, D::Error> {
    use serde::de::Error;
    #[derive(Deserialize)]
    #[serde(untagged)]
    enum NumOrStr {
        Num(f64),
        Str(String),
    }
    match NumOrStr::deserialize(d)? {
        NumOrStr::Num(n) => Ok(n),
        NumOrStr::Str(s) => s.parse().map_err(D::Error::custom),
    }
}
