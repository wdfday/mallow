//! OKX WebSocket candlestick feed — multi-timeframe.
//!
//! Subscribes to `candle{interval}` channels for every symbol × TF on a
//! single connection. Each push is forwarded as a [`BarEvent`]:
//! - `row[8] == "0"` → forming bar  (`closed = false`)
//! - `row[8] == "1"` → confirmed bar (`closed = true`)
//!
//! # OKX confirm delay
//!
//! OKX deliberately delays the `confirm=1` flag by ~30 seconds after the bar
//! closes to allow late-arriving trades to be included. Waiting for it would
//! send strategy signals 30s late.
//!
//! We apply an **auto-close** heuristic: when a forming bar arrives with a
//! timestamp strictly greater than the previously tracked forming bar, the
//! previous bar has definitively closed — we emit it as `closed=true`
//! immediately (typically within 500ms of the bar boundary). OKX's delayed
//! `confirm=1` is still forwarded but the ledger's dup-detection silently
//! skips it (`Ok(None)` — out-of-order/dup).

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use alm_core::Timeframe;
use alm_ledger::Ledger;
use futures::{SinkExt, StreamExt};
use metrics::{counter, gauge};
use serde::{Deserialize, Serialize};
use tokio_tungstenite::{connect_async, tungstenite::Message};
use tracing::{debug, error, info, warn};

use super::{BarEvent, BarTx};
use super::rest::{gap_fill_symbol, Exchange};

const WS_URL: &str = "wss://ws.okx.com:8443/ws/v5/business";
const RECONNECT: Duration = Duration::from_secs(5);
const PING_INTERVAL: Duration = Duration::from_secs(25);
/// Max silence before assuming a dead connection and forcing reconnect.
/// OKX pong replies reset this; 60 s gives two missed ping cycles before acting.
const READ_TIMEOUT: Duration = Duration::from_secs(60);
const RECONNECT_GAP_FILL_CONCURRENCY: usize = 3;

/// Spawn a task that streams candlesticks for `symbol_tfs` on a single OKX connection.
///
/// `symbol_tfs`: per-symbol `(raw_symbol, Vec<Timeframe>)` from [`SymbolConfig::okx_symbol_tfs`].
/// All symbols × their declared TFs are subscribed in a single `subscribe` message on connect.
///
/// Auto-reconnects on disconnect; runs a REST gap-fill before resuming the WS loop.
/// Task exits when `tx` is dropped (receiver gone).
pub fn spawn(symbol_tfs: Vec<(String, Vec<Timeframe>)>, tx: BarTx, ledger: Arc<Ledger>, tf: Timeframe) {
    if symbol_tfs.is_empty() {
        return;
    }
    tokio::spawn(async move {
        // Persisted across reconnects so auto-close state survives brief disconnects.
        // On a long outage the gap-fill REST fetch bridges the missing bars anyway.
        let mut last_forming: HashMap<String, (i64, alm_core::Bar)> = HashMap::new();
        let mut reconnect_n: u32 = 0;
        loop {
            if reconnect_n > 0 {
                counter!("herald_feed_reconnects_total", "source" => "okx").increment(1);
                info!(symbols = symbol_tfs.len(), reconnect_n, "okx: reconnect gap-fill");
                let sem = Arc::new(tokio::sync::Semaphore::new(RECONNECT_GAP_FILL_CONCURRENCY));
                let tasks: Vec<_> = symbol_tfs.iter().map(|(sym, tfs)| {
                    let live_sym = format!("okx:{sym}");
                    let last_ts = ledger.with_state(&live_sym, tf, |s| s.last_ts).flatten();
                    let base_from_ms = last_ts.map(|ts| ts + tf.duration_ms());
                    let ledger = ledger.clone();
                    let sym = sym.clone();
                    let tfs = tfs.clone();
                    let sem = sem.clone();
                    async move {
                        let _permit = sem.acquire_owned().await.expect("semaphore closed");
                        gap_fill_symbol(&ledger, tf, &format!("okx:{sym}"), &sym, Exchange::Okx, base_from_ms, &tfs).await;
                    }
                }).collect();
                futures::future::join_all(tasks).await;
            }
            match run_once(&symbol_tfs, &tx, &mut last_forming).await {
                Ok(()) => {
                    gauge!("herald_feed_ws_connected", "source" => "okx").set(0.0);
                    info!(symbols = symbol_tfs.len(), reconnect_n, "okx: feed task exiting (bar channel closed)");
                    return;
                }
                Err(e) => {
                    gauge!("herald_feed_ws_connected", "source" => "okx").set(0.0);
                    if tx.is_closed() {
                        info!(symbols = symbol_tfs.len(), reconnect_n, "okx: feed task exiting (bar channel closed after error)");
                        return;
                    }
                    reconnect_n += 1;
                    warn!(err = %e, reconnect_n, wait_secs = RECONNECT.as_secs(), "okx: disconnected, reconnecting");
                    tokio::time::sleep(RECONNECT).await;
                }
            }
        }
    });
}

async fn run_once(
    symbol_tfs: &[(String, Vec<Timeframe>)],
    tx: &BarTx,
    last_forming: &mut HashMap<String, (i64, alm_core::Bar)>,
) -> anyhow::Result<()> {
    let (ws, _) = connect_async(WS_URL).await?;
    info!("okx: connected");
    gauge!("herald_feed_ws_connected", "source" => "okx").set(1.0);
    let (mut write, mut read) = ws.split();

    // Subscribe to candle{interval} for every symbol × its declared TFs.
    let args: Vec<SubArg> = symbol_tfs
        .iter()
        .flat_map(|(sym, tfs)| {
            tfs.iter().filter_map(|&t| {
                tf_to_channel(t).map(|ch| SubArg { channel: ch.to_string(), inst_id: sym.clone() })
            })
        })
        .collect();
    let n = args.len();
    let sub = SubRequest { op: "subscribe", args };
    write.send(Message::Text(serde_json::to_string(&sub)?.into())).await?;
    let total_tfs: usize = symbol_tfs.iter().map(|(_, tfs)| tfs.len()).sum();
    info!(symbols = symbol_tfs.len(), total_tfs, subscriptions = n, "okx: subscribed");

    let mut ping_interval = tokio::time::interval(PING_INTERVAL);
    ping_interval.tick().await; // skip first immediate tick

    // Deadline resets on every received message (data or pong).
    // If nothing arrives for READ_TIMEOUT we assume a half-dead connection.
    let mut read_deadline = tokio::time::Instant::now() + READ_TIMEOUT;

    loop {
        tokio::select! {
            _ = ping_interval.tick() => {
                if write.send(Message::Text("ping".into())).await.is_err() {
                    return Err(anyhow::anyhow!("ping failed"));
                }
            }
            _ = tokio::time::sleep_until(read_deadline) => {
                warn!("okx: read timeout ({READ_TIMEOUT:?}) — reconnecting");
                return Err(anyhow::anyhow!("read timeout"));
            }
            msg = read.next() => {
                read_deadline = tokio::time::Instant::now() + READ_TIMEOUT;
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
                    Message::Text(t) if t == "pong" => {
                        debug!("okx: pong received");
                    }
                    Message::Text(t) => handle_push(&t, tx, &mut write, last_forming).await?,
                    Message::Ping(d) => { let _ = write.send(Message::Pong(d)).await; }
                    Message::Close(frame) => {
                        warn!(reason = ?frame, "okx: server sent Close frame");
                        return Err(anyhow::anyhow!("server closed connection"));
                    }
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
    last_forming: &mut HashMap<String, (i64, alm_core::Bar)>,
) -> anyhow::Result<()> {
    // Capture arrival time before JSON parse.
    let received_at_ms = super::now_ms();

    // OKX sends subscription acks and error events that are not candle pushes.
    // Parse them explicitly so nothing is silently swallowed.
    let Ok(push) = serde_json::from_str::<CandlePush>(text) else {
        // Log non-data frames at debug so we can diagnose unexpected messages.
        // Known expected frames: {"event":"subscribe",...}, {"event":"error",...}.
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(text) {
            if let Some(event) = v.get("event").and_then(|e| e.as_str()) {
                if event == "error" {
                    error!(frame = text, "okx: error event from server");
                } else {
                    debug!(event, "okx: non-data frame");
                }
            } else {
                debug!(frame = %&text[..text.len().min(200)], "okx: unparseable frame");
            }
        }
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
        let confirmed_by_okx = row[8] == "1";
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

        // Tracker key: one slot per (instId, tf) pair.
        let key = format!("{}:{:?}", push.arg.inst_id, tf);

        if confirmed_by_okx {
            // OKX's official confirm (~30s late).  Clear the forming slot only
            // when it still holds the same bar (low-volume case where confirm
            // arrives before the next bar starts forming).
            if matches!(last_forming.get(&key), Some((stored_ts, _)) if *stored_ts == ts) {
                last_forming.remove(&key);
            }
            debug!(symbol = %bar.symbol, ?tf, close = bar.close, "okx: bar closed (exchange confirm)");
            counter!("herald_feed_bars_total", "source" => "okx", "symbol" => push.arg.inst_id.clone()).increment(1);
            // Ledger dedup (Ok(None)) handles the case where we already
            // auto-closed this bar when the next bar started forming.
            if tx.send(BarEvent { tf, bar, closed: true, received_at_ms }).await.is_err() {
                let _ = write.close().await;
                return Ok(());
            }
        } else {
            // Forming bar.  If a newer timestamp arrives the previous bar is
            // definitively closed — emit it immediately without waiting for
            // OKX's delayed confirm=1.
            if let Some((prev_ts, prev_bar)) = last_forming.remove(&key) {
                if ts > prev_ts {
                    let delay_ms = received_at_ms - (prev_ts + tf.duration_ms());
                    info!(
                        symbol = %prev_bar.symbol, ?tf, close = prev_bar.close,
                        delay_ms,
                        "okx: bar auto-closed (next bar arrived)"
                    );
                    counter!("herald_feed_bars_total", "source" => "okx", "symbol" => push.arg.inst_id.clone()).increment(1);
                    counter!("herald_feed_bars_auto_closed_total", "source" => "okx").increment(1);
                    if tx.send(BarEvent { tf, bar: prev_bar, closed: true, received_at_ms }).await.is_err() {
                        let _ = write.close().await;
                        return Ok(());
                    }
                }
                // ts == prev_ts: same bar, fall through to update stored entry.
                // ts < prev_ts: out-of-order (shouldn't happen), discard old slot.
            }
            // Store the latest forming bar data and forward as live update.
            last_forming.insert(key, (ts, bar.clone()));
            if tx.send(BarEvent { tf, bar, closed: false, received_at_ms }).await.is_err() {
                let _ = write.close().await;
                return Ok(());
            }
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
