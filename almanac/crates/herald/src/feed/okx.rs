use std::time::Duration;

use alm_core::Bar;
use futures::{SinkExt, StreamExt};
use serde::{Deserialize, Serialize};
use tokio_tungstenite::{connect_async, tungstenite::Message};
use tracing::{debug, info, warn};

use super::BarTx;

const WS_URL: &str = "wss://ws.okx.com:8443/ws/v5/business";
const RECONNECT: Duration = Duration::from_secs(5);
const PING_INTERVAL: Duration = Duration::from_secs(25);

/// Spawn a task that streams confirmed M1 candles for `symbols` from OKX.
/// Sends completed `Bar`s to `tx`. Reconnects automatically on disconnect.
/// Task exits when `tx` is dropped (receiver gone).
pub fn spawn(symbols: Vec<String>, tx: BarTx) {
    if symbols.is_empty() {
        return;
    }
    tokio::spawn(async move {
        loop {
            match run_once(&symbols, &tx).await {
                Ok(()) => return,
                Err(e) => {
                    if tx.is_closed() {
                        return;
                    }
                    warn!(err = %e, wait_secs = RECONNECT.as_secs(), "okx: disconnected, reconnecting");
                    tokio::time::sleep(RECONNECT).await;
                }
            }
        }
    });
}

async fn run_once(symbols: &[String], tx: &BarTx) -> anyhow::Result<()> {
    let (ws, _) = connect_async(WS_URL).await?;
    info!("okx: connected");
    let (mut write, mut read) = ws.split();

    // Subscribe to candle1m for each symbol.
    let sub = SubRequest {
        op: "subscribe",
        args: symbols
            .iter()
            .map(|s| SubArg { channel: "candle1m", inst_id: s.as_str() })
            .collect(),
    };
    write.send(Message::Text(serde_json::to_string(&sub)?)).await?;

    // Ping ticker
    let mut ping_interval = tokio::time::interval(PING_INTERVAL);
    ping_interval.tick().await; // skip first immediate tick

    loop {
        tokio::select! {
            _ = ping_interval.tick() => {
                if write.send(Message::Text("ping".into())).await.is_err() {
                    return Err(anyhow::anyhow!("ping failed"));
                }
            }
            msg = read.next() => {
                let msg = match msg {
                    Some(Ok(m)) => m,
                    Some(Err(e)) => {
                        if tx.is_closed() {
                            let _ = write.close().await;
                            return Ok(());
                        }
                        return Err(e.into());
                    }
                    None => return Err(anyhow::anyhow!("stream ended")),
                };

                match msg {
                    Message::Text(t) if t == "pong" => {}
                    Message::Text(t) => handle_candle(&t, tx, &mut write).await?,
                    Message::Ping(d) => { let _ = write.send(Message::Pong(d)).await; }
                    Message::Close(_) => return Err(anyhow::anyhow!("server closed")),
                    _ => {}
                }
            }
        }
    }
}

async fn handle_candle(
    text: &str,
    tx: &BarTx,
    write: &mut (impl futures::Sink<Message> + Unpin),
) -> anyhow::Result<()> {
    let Ok(push) = serde_json::from_str::<CandlePush>(text) else {
        return Ok(());
    };
    if push.arg.channel != "candle1m" || push.data.is_empty() {
        return Ok(());
    }

    for row in &push.data {
        // row: [ts, open, high, low, close, vol, volCcy, volCcyQuote, confirm]
        if row.len() < 9 || row[8] != "1" {
            continue; // in-progress candle
        }
        let ts: i64 = row[0].parse().unwrap_or(0);
        let bar = Bar::new(
            ts,
            &push.arg.inst_id,
            parse_f64(&row[1]),
            parse_f64(&row[2]),
            parse_f64(&row[3]),
            parse_f64(&row[4]),
            parse_f64(&row[5]),
        );
        debug!(symbol = %bar.symbol, close = bar.close, "okx: bar closed");
        if tx.send(bar).is_err() {
            let _ = write.close().await;
            return Ok(());
        }
    }
    Ok(())
}

fn parse_f64(s: &str) -> f64 {
    s.parse().unwrap_or(0.0)
}

// ── JSON serialization / deserialization ─────────────────────────────────────

#[derive(Serialize)]
struct SubRequest<'a> {
    op: &'a str,
    args: Vec<SubArg<'a>>,
}

#[derive(Serialize)]
struct SubArg<'a> {
    channel: &'a str,
    #[serde(rename = "instId")]
    inst_id: &'a str,
}

#[derive(Deserialize)]
struct CandlePush {
    arg: PushArg,
    #[serde(default)]
    data: Vec<Vec<String>>,
}

#[derive(Deserialize)]
struct PushArg {
    channel: String,
    #[serde(rename = "instId")]
    inst_id: String,
}
