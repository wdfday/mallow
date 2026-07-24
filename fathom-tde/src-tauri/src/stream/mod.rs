//! Real-time bar streaming — gateway's `/api/v1/stream` WebSocket, replacing (for the chart's
//! live tail) the 10s REST poll `data-service.ts::getLatestBar` used before this.
//!
//! Protocol, verified against the actual Go source (`api-gateway/internal/ws/{hub,conn,protocol}.go`)
//! rather than assumed from the design doc, which drifted in a couple of places:
//! - Auth: normally a browser negotiates via the `Sec-WebSocket-Protocol: bearer, <jwt>` subprotocol
//!   trick (browsers can't set arbitrary headers on a WS handshake). A native client doesn't have
//!   that restriction — `WSBearerFromProtocol` middleware no-ops when an `Authorization` header is
//!   already present on the handshake request, so we just send `Authorization: Bearer <token>`
//!   directly and skip subprotocols entirely.
//! - Market data (`bars`) is plain NATS core pub/sub relayed over the socket — no JetStream
//!   replay, no snapshot-on-subscribe, and (unlike account channels) the server does not remember
//!   subscriptions across a reconnect. Every `subscribe` must be resent after each reconnect,
//!   which is exactly what the "wanted" set below is for.
//! - Wire key is `{tf}:{source}:{symbol}` (three colon-delimited segments, tf first) — e.g.
//!   `M1:binance:BTCUSDT` — not the two-part `{source}:{symbol}` an earlier pass assumed.
//! - Binary frame: `[1 byte tag][2 bytes big-endian key length][key utf8][raw protobuf BarMsg]`,
//!   tag `0x01` = closed bar, `0x02` = forming (still-live) bar.
//! - Reconnect backoff mirrors mallow-client's reference implementation exactly: a fixed table
//!   `[1, 2, 5, 10, 15]` seconds, holding at 15s for all further attempts, retried indefinitely
//!   until the caller explicitly disconnects.

use std::collections::HashSet;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use futures_util::{SinkExt, StreamExt};
use prost::Message as ProstMessage;
use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager, State};
use tokio::net::TcpStream;
use tokio::sync::mpsc;
use tokio_tungstenite::tungstenite::client::IntoClientRequest;
use tokio_tungstenite::tungstenite::http::StatusCode;
use tokio_tungstenite::tungstenite::{Error as TungsteniteError, Message as WsMessage};
use tokio_tungstenite::{MaybeTlsStream, WebSocketStream};

use crate::auth::{current_token_and_expiry, gateway_url, refresh_session, AuthState};

type WsStream = WebSocketStream<MaybeTlsStream<TcpStream>>;

/// A token that's rejected mid-dial and one that's about to expire need the same fix (refresh +
/// retry), but only the former is detected mid-connect — separated so `connect_once` can tell
/// "worth retrying with a fresh token" apart from any other connect failure (DNS, TLS, network).
enum ConnectError {
    Unauthorized,
    Other(String),
}

const RECONNECT_DELAYS_SECS: [u64; 5] = [1, 2, 5, 10, 15];
const TAG_BARS: u8 = 0x01;
const TAG_BARS_LIVE: u8 = 0x02;

#[derive(Clone, Eq, PartialEq, Hash, Debug)]
struct BarSub {
    tf: String,
    /// Herald token, e.g. `"binance:BTCUSDT"` — source lowercased, symbol byte-for-byte as given
    /// (confirmed: OKX's dash-form `BTC-USDT` is never transformed). Matches the wire/ack `key`
    /// once the `{tf}:` prefix is added.
    symbol: String,
}

struct SharedState {
    wanted: HashSet<BarSub>,
    writer: Option<mpsc::UnboundedSender<WsMessage>>,
    closed_by_user: bool,
    running: bool,
}

pub struct StreamState {
    shared: Arc<Mutex<SharedState>>,
}

impl StreamState {
    pub fn new() -> Self {
        Self {
            shared: Arc::new(Mutex::new(SharedState {
                wanted: HashSet::new(),
                writer: None,
                closed_by_user: false,
                running: false,
            })),
        }
    }
}

impl Default for StreamState {
    fn default() -> Self {
        Self::new()
    }
}

#[derive(Serialize, Clone)]
#[serde(rename_all = "camelCase")]
struct BarEvent {
    /// `"{tf}:{source}:{symbol}"` — the exact wire key, so the frontend can match without
    /// re-deriving it (avoids any risk of the two sides normalizing case/format differently).
    key: String,
    tf: String,
    source: String,
    symbol: String,
    /// True for a still-forming (live-tail) candle, false for one that just closed.
    forming: bool,
    time: i64,
    open: f64,
    high: f64,
    low: f64,
    close: f64,
    volume: f64,
}

#[derive(Serialize, Clone)]
#[serde(rename_all = "camelCase")]
struct StreamStatusEvent {
    connected: bool,
}

fn ws_url() -> Result<String, String> {
    let base = gateway_url();
    let ws_base = if let Some(rest) = base.strip_prefix("https://") {
        format!("wss://{rest}")
    } else if let Some(rest) = base.strip_prefix("http://") {
        format!("ws://{rest}")
    } else {
        return Err(format!("gateway url has no http(s) scheme: {base}"));
    };
    Ok(format!("{ws_base}/api/v1/stream"))
}

fn client_msg(op: &str, sub: &BarSub) -> String {
    serde_json::json!({ "op": op, "ch": "bars", "symbol": sub.symbol, "tf": sub.tf }).to_string()
}

/// Parses `[tag][u16 BE keylen][key][payload]`. `None` on a malformed (too-short) frame.
fn parse_frame(data: &[u8]) -> Option<(u8, String, &[u8])> {
    if data.len() < 3 {
        return None;
    }
    let tag = data[0];
    let key_len = u16::from_be_bytes([data[1], data[2]]) as usize;
    if data.len() < 3 + key_len {
        return None;
    }
    let key = std::str::from_utf8(&data[3..3 + key_len]).ok()?.to_string();
    Some((tag, key, &data[3 + key_len..]))
}

fn handle_binary(app: &AppHandle, data: &[u8]) {
    let Some((tag, key, payload)) = parse_frame(data) else { return };
    if tag != TAG_BARS && tag != TAG_BARS_LIVE {
        return;
    }
    let Ok(bar) = alm_core::BarMsg::decode(payload) else { return };
    // key = "{tf}:{source}:{symbol}" — split on the first two colons only, since an OKX symbol
    // itself is dash-form (no colons), and this keeps any hypothetical colon inside a future
    // symbol format from breaking the split.
    let mut parts = key.splitn(3, ':');
    let (Some(tf), Some(source), Some(symbol)) = (parts.next(), parts.next(), parts.next()) else { return };
    let event = BarEvent {
        key: key.clone(),
        tf: tf.to_string(),
        source: source.to_string(),
        symbol: symbol.to_string(),
        forming: tag == TAG_BARS_LIVE,
        time: bar.t / 1000,
        open: bar.o,
        high: bar.h,
        low: bar.l,
        close: bar.c,
        volume: bar.v,
    };
    let _ = app.emit("stream://bar", event);
}

/// Dials once with `token`. Distinguishes a 401 handshake rejection (`Error::Http` with that
/// status — tungstenite surfaces the raw HTTP response when the upgrade itself is refused) from
/// any other failure, so the caller can retry exactly the "token was stale/revoked" case with a
/// freshly refreshed token instead of treating it the same as a network/DNS/TLS error.
async fn try_connect(url: &str, token: &str) -> Result<WsStream, ConnectError> {
    let mut request = url.into_client_request().map_err(|e| ConnectError::Other(e.to_string()))?;
    let auth_value = format!("Bearer {token}")
        .parse()
        .map_err(|e: tokio_tungstenite::tungstenite::http::header::InvalidHeaderValue| ConnectError::Other(e.to_string()))?;
    request.headers_mut().insert("Authorization", auth_value);

    match tokio_tungstenite::connect_async(request).await {
        Ok((stream, _response)) => Ok(stream),
        Err(TungsteniteError::Http(resp)) if resp.status() == StatusCode::UNAUTHORIZED => Err(ConnectError::Unauthorized),
        Err(e) => Err(ConnectError::Other(e.to_string())),
    }
}

/// Proactive refresh (mirrors `gateway_fetch`'s reactive one) — a WS handshake gets exactly one
/// shot to auth, no per-message retry the way an HTTP call gets, so catching an about-to-expire
/// token before dialing avoids a guaranteed-401 connect attempt on every long-lived session.
async fn ensure_fresh_token(app: &AppHandle) -> Result<String, String> {
    let auth_state = app.state::<AuthState>();
    let (token, expires_at) = current_token_and_expiry(&auth_state)?;
    if expires_at - chrono::Utc::now().timestamp() > 30 {
        return Ok(token);
    }
    Ok(refresh_session(&auth_state).await?.token.access_token)
}

async fn connect_once(app: &AppHandle, shared: &Arc<Mutex<SharedState>>) -> Result<(), String> {
    let url = ws_url()?;
    let token = ensure_fresh_token(app).await?;

    let ws_stream = match try_connect(&url, &token).await {
        Ok(s) => s,
        Err(ConnectError::Unauthorized) => {
            // The proactive check above missed it (clock skew, or the server revoked the token
            // mid-life, e.g. a forced logout elsewhere) — refresh once and retry.
            let auth_state = app.state::<AuthState>();
            let session = refresh_session(&auth_state).await?;
            match try_connect(&url, &session.token.access_token).await {
                Ok(s) => s,
                Err(ConnectError::Unauthorized) => return Err("unauthorized even after refreshing the token".to_string()),
                Err(ConnectError::Other(msg)) => return Err(msg),
            }
        }
        Err(ConnectError::Other(msg)) => return Err(msg),
    };
    let (mut write, mut read) = ws_stream.split();

    let (tx, mut rx) = mpsc::unbounded_channel::<WsMessage>();
    {
        let mut s = shared.lock().map_err(|_| "stream state poisoned")?;
        s.writer = Some(tx);
    }
    let _ = app.emit("stream://status", StreamStatusEvent { connected: true });

    // Resend every wanted subscription — the server has no memory of a previous connection's
    // subs, so this is required after EVERY (re)connect, not just the first one.
    {
        let s = shared.lock().map_err(|_| "stream state poisoned")?;
        if let Some(w) = &s.writer {
            for sub in &s.wanted {
                let _ = w.send(WsMessage::Text(client_msg("subscribe", sub).into()));
            }
        }
    }

    let result: Result<(), String> = loop {
        tokio::select! {
            incoming = read.next() => {
                match incoming {
                    Some(Ok(WsMessage::Binary(data))) => handle_binary(app, &data),
                    Some(Ok(WsMessage::Ping(payload))) => {
                        if write.send(WsMessage::Pong(payload)).await.is_err() {
                            break Err("failed to send pong".to_string());
                        }
                    }
                    Some(Ok(WsMessage::Close(_))) | None => break Ok(()),
                    Some(Ok(_)) => {} // text control frames (ack/error) — informational only, no client-side handling needed
                    Some(Err(e)) => break Err(e.to_string()),
                }
            }
            outgoing = rx.recv() => {
                match outgoing {
                    Some(msg) => {
                        if write.send(msg).await.is_err() {
                            break Err("failed to write to socket".to_string());
                        }
                    }
                    None => break Ok(()), // sender dropped — stream_disconnect tore it down
                }
            }
        }
    };

    if let Ok(mut s) = shared.lock() {
        s.writer = None;
    }
    let _ = app.emit("stream://status", StreamStatusEvent { connected: false });
    result
}

async fn run(app: AppHandle, shared: Arc<Mutex<SharedState>>) {
    let mut attempt = 0usize;
    loop {
        if shared.lock().map(|s| s.closed_by_user).unwrap_or(true) {
            break;
        }

        match connect_once(&app, &shared).await {
            Ok(()) => attempt = 0, // clean close — still backs off from scratch on the NEXT drop
            Err(e) => eprintln!("[stream] connection error: {e}"),
        }

        if shared.lock().map(|s| s.closed_by_user).unwrap_or(true) {
            break;
        }
        let delay = RECONNECT_DELAYS_SECS[attempt.min(RECONNECT_DELAYS_SECS.len() - 1)];
        tokio::time::sleep(Duration::from_secs(delay)).await;
        attempt += 1;
    }
    if let Ok(mut s) = shared.lock() {
        s.running = false;
    }
}

/// Starts the connect/reconnect loop if it isn't already running. Idempotent — safe to call on
/// every chart mount without worrying about a previous session still being alive.
#[tauri::command]
pub fn stream_connect(app: AppHandle, state: State<'_, StreamState>) -> Result<(), String> {
    let mut s = state.shared.lock().map_err(|_| "stream state poisoned")?;
    if s.running {
        return Ok(());
    }
    s.running = true;
    s.closed_by_user = false;
    let shared = state.shared.clone();
    tauri::async_runtime::spawn(run(app, shared));
    Ok(())
}

/// Adds `{source}:{symbol}`/`tf` to the wanted set (surviving reconnects) and sends `subscribe`
/// immediately if a socket is currently live.
#[tauri::command]
pub fn stream_subscribe_bars(state: State<'_, StreamState>, source: String, symbol: String, timeframe: String) -> Result<(), String> {
    let sub = BarSub { tf: timeframe, symbol: format!("{}:{symbol}", source.to_lowercase()) };
    let mut s = state.shared.lock().map_err(|_| "stream state poisoned")?;
    if let Some(w) = &s.writer {
        let _ = w.send(WsMessage::Text(client_msg("subscribe", &sub).into()));
    }
    s.wanted.insert(sub);
    Ok(())
}

#[tauri::command]
pub fn stream_unsubscribe_bars(state: State<'_, StreamState>, source: String, symbol: String, timeframe: String) -> Result<(), String> {
    let sub = BarSub { tf: timeframe, symbol: format!("{}:{symbol}", source.to_lowercase()) };
    let mut s = state.shared.lock().map_err(|_| "stream state poisoned")?;
    if let Some(w) = &s.writer {
        let _ = w.send(WsMessage::Text(client_msg("unsubscribe", &sub).into()));
    }
    s.wanted.remove(&sub);
    Ok(())
}

/// Explicit user-initiated shutdown — suppresses the reconnect loop (mirrors mallow-client's
/// `closedByUser` flag) rather than just letting the socket drop and reconnect forever.
#[tauri::command]
pub fn stream_disconnect(state: State<'_, StreamState>) -> Result<(), String> {
    let mut s = state.shared.lock().map_err(|_| "stream state poisoned")?;
    s.closed_by_user = true;
    s.writer = None; // dropping the sender ends the connect_once select loop on its next poll
    Ok(())
}
