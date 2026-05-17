//! OKX WebSocket candlestick feed — multi-timeframe.
//!
//! Subscribes to `candle{interval}` channels for every symbol × TF on a
//! single connection. Each push is forwarded as a [`BarEvent`]:
//! - `row[8] == "0"` → forming bar  (`closed = false`)
//! - `row[8] == "1"` → confirmed bar (`closed = true`)

use std::time::Duration;

use alm_core::Timeframe;
use futures::{SinkExt, StreamExt};
use serde::{Deserialize, Serialize};
use tokio_tungstenite::{connect_async, tungstenite::Message};
use tracing::{debug, info, warn};

use super::{BarEvent, BarTx, SUBSCRIBE_TFS};

const WS_URL: &str = "wss://ws.okx.com:8443/ws/v5/business";
const RECONNECT: Duration = Duration::from_secs(5);
const PING_INTERVAL: Duration = Duration::from_secs(25);

/// Spawn a task that streams candlesticks for `symbols` across all
/// [`SUBSCRIBE_TFS`]. Sends [`BarEvent`]s to `tx`. Auto-reconnects.
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

    // Subscribe to candle{interval} for every symbol × TF.
    let args: Vec<SubArg> = symbols
        .iter()
        .flat_map(|sym| {
            SUBSCRIBE_TFS.iter().filter_map(|&tf| {
                tf_to_channel(tf).map(|ch| SubArg { channel: ch.to_string(), inst_id: sym.clone() })
            })
        })
        .collect();
    let n = args.len();
    let sub = SubRequest { op: "subscribe", args };
    write.send(Message::Text(serde_json::to_string(&sub)?.into())).await?;
    info!(symbols = symbols.len(), tfs = SUBSCRIBE_TFS.len(), subscriptions = n, "okx: subscribed");

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
                    Message::Text(t) => handle_push(&t, tx, &mut write).await?,
                    Message::Ping(d) => { let _ = write.send(Message::Pong(d)).await; }
                    Message::Close(_) => return Err(anyhow::anyhow!("server closed")),
                    _ => {}
                }
            }
        }
    }
}

async fn handle_push(
    text: &str,
    tx: &BarTx,
    write: &mut (impl futures::Sink<Message> + Unpin),
) -> anyhow::Result<()> {
    let Ok(push) = serde_json::from_str::<CandlePush>(text) else {
        return Ok(());
    };
    let Some(tf) = channel_to_tf(&push.arg.channel) else {
        return Ok(());
    };
    if push.data.is_empty() {
        return Ok(());
    }

    for row in &push.data {
        // row: [ts, open, high, low, close, vol, volCcy, volCcyQuote, confirm]
        if row.len() < 9 {
            continue;
        }
        let closed = row[8] == "1";
        let ts: i64 = row[0].parse().unwrap_or(0);
        let bar = alm_core::Bar::new(
            ts,
            &format!("okx:{}", push.arg.inst_id),
            parse_f64(&row[1]),
            parse_f64(&row[2]),
            parse_f64(&row[3]),
            parse_f64(&row[4]),
            parse_f64(&row[5]),
        );
        if closed {
            debug!(symbol = %bar.symbol, ?tf, close = bar.close, "okx: bar closed");
        }
        if tx.send(BarEvent { tf, bar, closed }).is_err() {
            let _ = write.close().await;
            return Ok(());
        }
    }
    Ok(())
}

fn parse_f64(s: &str) -> f64 {
    s.parse().unwrap_or(0.0)
}

// ── Channel ↔ Timeframe ───────────────────────────────────────────────────────

fn tf_to_channel(tf: Timeframe) -> Option<&'static str> {
    match tf {
        Timeframe::M1  => Some("candle1m"),
        Timeframe::M3  => Some("candle3m"),
        Timeframe::M5  => Some("candle5m"),
        Timeframe::M15 => Some("candle15m"),
        Timeframe::M30 => Some("candle30m"),
        Timeframe::H1  => Some("candle1H"),
        Timeframe::H2  => Some("candle2H"),
        Timeframe::H4  => Some("candle4H"),
        Timeframe::H6  => Some("candle6H"),
        Timeframe::H12 => Some("candle12H"),
        Timeframe::D1  => Some("candle1D"),
        Timeframe::W1  => Some("candle1W"),
        Timeframe::MN  => Some("candle1M"),
        _ => None,
    }
}

fn channel_to_tf(ch: &str) -> Option<Timeframe> {
    match ch {
        "candle1m"  => Some(Timeframe::M1),
        "candle3m"  => Some(Timeframe::M3),
        "candle5m"  => Some(Timeframe::M5),
        "candle15m" => Some(Timeframe::M15),
        "candle30m" => Some(Timeframe::M30),
        "candle1H"  => Some(Timeframe::H1),
        "candle2H"  => Some(Timeframe::H2),
        "candle4H"  => Some(Timeframe::H4),
        "candle6H"  => Some(Timeframe::H6),
        "candle12H" => Some(Timeframe::H12),
        "candle1D"  => Some(Timeframe::D1),
        "candle1W"  => Some(Timeframe::W1),
        "candle1M"  => Some(Timeframe::MN),
        _ => None,
    }
}

// ── JSON serialization / deserialization ──────────────────────────────────────

#[derive(Serialize)]
struct SubRequest {
    op: &'static str,
    args: Vec<SubArg>,
}

#[derive(Serialize)]
struct SubArg {
    channel: String,
    #[serde(rename = "instId")]
    inst_id: String,
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
