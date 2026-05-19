//! Bar ingestion + NATS control plane.
//!
//! Architecture (WebSocket-native, multi-TF):
//!
//! ```text
//!   feed::binance  ─┐
//!   feed::okx      ─┤  mpsc::UnboundedSender<BarEvent { tf, bar, closed }>
//!                   ▼
//!              Handler::run
//!                   │
//!          closed? ─┼──────────────────────────────────────┐
//!          yes      │                                       │ no (forming)
//!                   ▼                                       ▼
//!          Ledger::advance(tf, bar)             Ledger::advance_live(tf, bar)
//!                   │                           (no observer notification)
//!                   ▼ fan-out (base TF only)
//!            LedgerObserver(s)
//!                   │
//!             Registry (mpsc)  ──→ signal_publisher → NATS "signals"
//!                   │
//!           (base TF closed) ──→ BarRing + SSE bcast + NATS bars.{symbol}
//! ```

use std::sync::Arc;
use std::time::Instant;

use alm_strategy::build_strategy;
use alm_core::msg::{
    BarMsg, HandInfo, HandListResponse, DeregisterMsg, HeartbeatRequest, HeartbeatResponse,
    PingResponse, ReadyEvent, RegisterMsg, ResetMsg, SignalMsg, SignalResponse,
};
use alm_core::{Bar, Timeframe};
use alm_ledger::Ledger;
use async_nats::{Client, jetstream};
use futures::StreamExt;
use prost::Message as _;
use tokio::sync::{broadcast, mpsc};
use tracing::{debug, error, info, warn};
use uuid::Uuid;

use crate::feed::{BarEvent, BarRx};
use crate::registry::{HandSignal, Registry};
use crate::ring::BarRing;

// ── NATS subjects ─────────────────────────────────────────────────────────────

pub const SUBJ_RESET: &str = "engine.reset";
pub const SUBJ_REGISTER: &str = "engine.register";
pub const SUBJ_DEREGISTER: &str = "engine.deregister";
pub const SUBJ_LIST: &str = "engine.list";
pub const SUBJ_PING: &str = "engine.ping";
pub const SUBJ_HEARTBEAT: &str = "engine.heartbeat";
pub const SUBJ_READY: &str = "engine.ready";
pub const SUBJ_SIGNALS: &str = "signals";
pub const SUBJ_BARS: &str = "bars";

// ── Handler ───────────────────────────────────────────────────────────────────

pub struct Handler {
    client: Client,
    ledger: Arc<Ledger>,
    registry: Arc<Registry>,
    ring: BarRing,
    /// Base timeframe — only closed bars at this TF trigger NATS publish +
    /// SSE broadcast + Registry observer fan-out.
    tf: Timeframe,
    bar_rx: BarRx,
    signal_rx: Option<mpsc::UnboundedReceiver<HandSignal>>,
    bar_bcast: broadcast::Sender<Bar>,
    sig_bcast: broadcast::Sender<Arc<HandSignal>>,
    /// Stable per-instance identifier — changes on every restart so consumers
    /// can detect herald restart and re-register their hands.
    herald_id: String,
    start_time: Instant,
}

impl Handler {
    pub fn new(
        client: Client,
        ledger: Arc<Ledger>,
        registry: Arc<Registry>,
        ring: BarRing,
        tf: Timeframe,
        bar_rx: BarRx,
        signal_rx: mpsc::UnboundedReceiver<HandSignal>,
        bar_bcast: broadcast::Sender<Bar>,
        sig_bcast: broadcast::Sender<Arc<HandSignal>>,
    ) -> Self {
        Self {
            client,
            ledger,
            registry,
            ring,
            tf,
            bar_rx,
            signal_rx: Some(signal_rx),
            bar_bcast,
            sig_bcast,
            herald_id: Uuid::now_v7().to_string(),
            start_time: Instant::now(),
        }
    }

    pub async fn run(mut self) -> anyhow::Result<()> {
        let rx = self.signal_rx.take().expect("signal_rx present at run()");
        tokio::spawn(signal_publisher(self.client.clone(), rx, self.sig_bcast.clone()));

        let mut reset_sub      = self.client.subscribe(SUBJ_RESET).await?;
        let mut register_sub   = self.client.subscribe(SUBJ_REGISTER).await?;
        let mut deregister_sub = self.client.subscribe(SUBJ_DEREGISTER).await?;
        let mut list_sub       = self.client.subscribe(SUBJ_LIST).await?;
        let mut ping_sub       = self.client.subscribe(SUBJ_PING).await?;
        let mut heartbeat_sub  = self.client.subscribe(SUBJ_HEARTBEAT).await?;

        // Announce availability — consumers subscribe to engine.ready and re-register
        // all running hands when they see a new herald_id (i.e. after a restart).
        self.publish_ready().await;

        info!(
            herald_id = %self.herald_id,
            tf = ?self.tf,
            "herald ready (WebSocket ingestion mode)"
        );

        loop {
            tokio::select! {
                Some(event) = self.bar_rx.recv()        => self.handle_bar_event(event).await,
                Some(msg) = reset_sub.next()            => self.handle_reset(msg).await,
                Some(msg) = register_sub.next()         => self.handle_register(msg).await,
                Some(msg) = deregister_sub.next()       => self.handle_deregister(msg).await,
                Some(msg) = list_sub.next()             => self.handle_list(msg).await,
                Some(msg) = ping_sub.next()             => self.handle_ping(msg).await,
                Some(msg) = heartbeat_sub.next()        => self.handle_heartbeat(msg).await,
                else => break,
            }
        }
        Ok(())
    }

    async fn publish_ready(&self) {
        let mut symbols: Vec<String> = self.registry.list_hands()
            .into_iter()
            .map(|(_, _, sym, _, _)| sym)
            .collect::<std::collections::HashSet<_>>()
            .into_iter()
            .collect();
        symbols.sort();
        let ev = ReadyEvent {
            herald_id: self.herald_id.clone(),
            ts: chrono::Utc::now().timestamp_millis(),
            tf: self.tf.to_string(),
            symbols,
        };
        let payload = ev.encode_to_vec();
        if let Err(e) = self.client.publish(SUBJ_READY, payload.into()).await {
            warn!(err = %e, "failed to publish engine.ready");
        }
    }

    // ── bar event ─────────────────────────────────────────────────────────────

    async fn handle_bar_event(&self, event: BarEvent) {
        let BarEvent { tf, bar, closed } = event;
        let symbol = bar.symbol.clone();
        let ts = bar.timestamp;

        // Forming bar — update live_bar in ledger only, no observer fan-out.
        if !closed {
            self.ledger.advance_live(tf, bar);
            return;
        }

        // Confirmed bar — advance the ledger slice for this TF.
        match self.ledger.advance(tf, bar.clone()) {
            Ok(Some(_)) => debug!(
                %symbol, ?tf, bar_ts = ts,
                open = bar.open, high = bar.high, low = bar.low,
                close = bar.close, vol = bar.volume,
                "bar confirmed"
            ),
            Ok(None)    => debug!(%symbol, ?tf, bar_ts = ts, "bar skipped (dup/out-of-order)"),
            Err(e)      => error!(%symbol, ?tf, bar_ts = ts, err = %e, "ledger.advance failed"),
        }

        // Base TF only: push to ring, SSE broadcast, NATS publish.
        if tf != self.tf {
            return;
        }

        // 1. Push to 24h ring buffer.
        self.ring.push(bar.clone());

        // 2. Broadcast to SSE subscribers.
        let _ = self.bar_bcast.send(bar.clone());

        // 3. Publish to NATS bars.{symbol} for downstream consumers.
        let bar_msg = BarMsg::from(&bar);
        let payload = bar_msg.encode_to_vec();
        let subject = format!("{}.{}", SUBJ_BARS, symbol);
        if let Err(e) = self.client.publish(subject.clone(), payload.into()).await {
            error!(%symbol, err = %e, "failed to publish bar to NATS");
        } else {
            debug!(%symbol, nats_subject = %subject, "bar published to NATS");
        }
    }

    // ── engine.configure ─────────────────────────────────────────────────────

    // ── engine.reset ─────────────────────────────────────────────────────────

    async fn handle_reset(&self, msg: async_nats::Message) {
        let reset = match ResetMsg::decode(msg.payload.as_ref()) {
            Ok(r) => r,
            Err(e) => { error!(err = %e, "failed to decode ResetMsg"); return; }
        };
        let symbol = reset.symbol.as_str();
        if symbol.is_empty() { info!("resetting all symbols"); }
        else                  { info!(%symbol, "resetting symbol"); }
        self.registry.reset(symbol);
    }

    // ── engine.register ──────────────────────────────────────────────────────

    async fn handle_register(&self, msg: async_nats::Message) {
        let req = match RegisterMsg::decode(msg.payload.as_ref()) {
            Ok(r) => r,
            Err(e) => { error!(err = %e, "failed to decode RegisterMsg"); return; }
        };
        info!(hand_id = %req.hand_id, symbol = %req.symbol, "registering hand");
        // Timeframe is required — strategies are TF-specific so silent fallback
        // to herald's base TF would run a script designed for H1 on M1 bars.
        let _target_tf = match req.timeframe.as_str() {
            "" => {
                let err = "timeframe is required (e.g. \"M1\", \"M15\", \"H1\")";
                warn!(hand_id = %req.hand_id, "register rejected: {err}");
                if let Some(reply) = msg.reply {
                    let ack = serde_json::json!({"ok": false, "error": err}).to_string();
                    let _ = self.client.publish(reply, ack.into_bytes().into()).await;
                }
                return;
            }
            s => match parse_timeframe_str(s) {
                Some(tf) => tf,
                None => {
                    let err = format!(
                        "invalid timeframe `{s}` (supported: M1, M3, M5, M10, M15, M30, H1, H2, H4, H6, H12, D1, W1)"
                    );
                    warn!(hand_id = %req.hand_id, "register rejected: {err}");
                    if let Some(reply) = msg.reply {
                        let ack = serde_json::json!({"ok": false, "error": err}).to_string();
                        let _ = self.client.publish(reply, ack.into_bytes().into()).await;
                    }
                    return;
                }
            },
        };
        // Validate the script BEFORE touching the registry — try a dry-run
        // build. This catches: script syntax errors, indicator typos
        // (`ind.mma` etc.), invalid `candle.transform(...)` directives,
        // misplaced directives, unbalanced `regime { ... }` block, duplicate
        // indicator names. Doing this here avoids the side-effect leak from
        // `registry.register()` calling `ledger.ensure_symbol(...)` before
        // build fails — and gives the caller the error in the ack instead of
        // a generic registry error.
        let probe = serde_json::json!({ "script": req.script, "_live": true });
        if let Err(e) = build_strategy("script", &probe) {
            let err = format!("script validation failed: {e}");
            warn!(hand_id = %req.hand_id, "register rejected: {err}");
            if let Some(reply) = msg.reply {
                let ack = serde_json::json!({"ok": false, "error": err}).to_string();
                let _ = self.client.publish(reply, ack.into_bytes().into()).await;
            }
            return;
        }

        let result = self.registry.register(
            req.hand_id.clone(), req.helm_id.clone(), req.symbol.clone(),
            req.script.clone(),
        );
        if let Some(reply) = msg.reply {
            let ack = match result {
                Ok(()) => {
                    info!(hand_id = %req.hand_id, symbol = %req.symbol, "hand registered");
                    serde_json::json!({"ok": true}).to_string()
                }
                Err(e) => {
                    warn!(hand_id = %req.hand_id, err = %e, "failed to register hand");
                    serde_json::json!({"ok": false, "error": e.to_string()}).to_string()
                }
            };
            let _ = self.client.publish(reply, ack.into_bytes().into()).await;
        }
    }

    // ── engine.deregister ────────────────────────────────────────────────────

    async fn handle_deregister(&self, msg: async_nats::Message) {
        let req = match DeregisterMsg::decode(msg.payload.as_ref()) {
            Ok(r) => r,
            Err(e) => { error!(err = %e, "failed to decode DeregisterMsg"); return; }
        };
        let hand_id = req.hand_id.as_str();
        if hand_id.is_empty() { info!("deregistering ALL hands"); }
        else                  { info!(%hand_id, "deregistering hand"); }
        self.registry.deregister(hand_id);
        if let Some(reply) = msg.reply {
            let ack = serde_json::json!({"ok": true}).to_string();
            let _ = self.client.publish(reply, ack.into_bytes().into()).await;
        }
    }

    // ── engine.list ──────────────────────────────────────────────────────────

    async fn handle_list(&self, msg: async_nats::Message) {
        let Some(reply) = msg.reply else {
            warn!("engine.list without reply subject — ignoring");
            return;
        };
        let hands = self.registry.list_hands().into_iter()
            .map(|(hand_id, helm_id, symbol, script, timeframe)| {
                HandInfo { hand_id, symbol, script, timeframe, helm_id }
            })
            .collect();
        let payload = HandListResponse { hands }.encode_to_vec();
        let _ = self.client.publish(reply, payload.into()).await;
    }

    // ── engine.ping ───────────────────────────────────────────────────────────

    async fn handle_ping(&self, msg: async_nats::Message) {
        let Some(reply) = msg.reply else { return; };
        let resp = PingResponse {
            ok: true,
            hands: self.registry.hand_count() as i32,
            uptime_ms: self.start_time.elapsed().as_millis() as i64,
            herald_id: self.herald_id.clone(),
        };
        let _ = self.client.publish(reply, resp.encode_to_vec().into()).await;
    }

    // ── engine.heartbeat ─────────────────────────────────────────────────────

    async fn handle_heartbeat(&self, msg: async_nats::Message) {
        let Some(reply) = msg.reply else { return; };
        let req = match HeartbeatRequest::decode(msg.payload.as_ref()) {
            Ok(r) => r,
            Err(e) => {
                warn!(err = %e, "failed to decode HeartbeatRequest");
                return;
            }
        };
        let all_hands = self.registry.list_hands();
        let registered_set: std::collections::HashSet<&str> =
            all_hands.iter().map(|(id, ..)| id.as_str()).collect();
        let mut missing = Vec::new();
        let mut registered = Vec::new();
        for hand_id in &req.hands {
            if registered_set.contains(hand_id.as_str()) {
                registered.push(hand_id.clone());
            } else {
                missing.push(hand_id.clone());
            }
        }
        if !missing.is_empty() {
            warn!(helm_id = %req.helm_id, missing = ?missing, "heartbeat: hands missing from registry");
        }
        let resp = HeartbeatResponse { ok: missing.is_empty(), missing, registered };
        let _ = self.client.publish(reply, resp.encode_to_vec().into()).await;
    }
}

// ── Signal publisher task ─────────────────────────────────────────────────────

async fn signal_publisher(
    client: Client,
    mut rx: mpsc::UnboundedReceiver<HandSignal>,
    bcast: broadcast::Sender<Arc<HandSignal>>,
) {
    let js = jetstream::new(client);

    // Ensure the SIGNALS stream exists before publishing.
    // Helm creates it on startup, but herald may start before helm is ready.
    let stream_cfg = jetstream::stream::Config {
        name: "SIGNALS".to_string(),
        subjects: vec![SUBJ_SIGNALS.to_string()],
        storage: jetstream::stream::StorageType::Memory,
        max_age: std::time::Duration::from_secs(60),
        max_messages: 10_000,
        ..Default::default()
    };
    match js.get_or_create_stream(stream_cfg).await {
        Ok(_) => info!("SIGNALS JetStream stream ready"),
        Err(e) => warn!(err = %e, "SIGNALS stream ensure failed — helm may not be up yet; will retry on each publish"),
    }

    while let Some(batch) = rx.recv().await {
        let batch = Arc::new(batch);
        let _ = bcast.send(Arc::clone(&batch));

        let response = SignalResponse {
            signal: Some(SignalMsg::from(&batch.signal)),
            helm_id: batch.helm_id.clone(),
            hand_id: batch.hand_id.clone(),
        };
        let payload = response.encode_to_vec();
        debug!(hand_id = %batch.hand_id, symbol = %batch.signal.symbol, "publishing signal to JetStream");
        match js.publish(SUBJ_SIGNALS, payload.into()).await {
            Ok(ack_future) => {
                if let Err(e) = ack_future.await {
                    error!(subject = SUBJ_SIGNALS, err = %e, "JetStream signal ack failed");
                } else {
                    info!(
                        hand_id = %batch.hand_id, symbol = %batch.signal.symbol,
                        direction = ?batch.signal.direction, strength = batch.signal.strength,
                        bar_ts = batch.bar_ts, "signal emitted to JetStream"
                    );
                }
            }
            Err(e) => error!(subject = SUBJ_SIGNALS, err = %e, "JetStream signal publish failed"),
        }
    }
    info!("signal publisher channel closed");
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn parse_timeframe_str(s: &str) -> Option<Timeframe> {
    match s.to_ascii_uppercase().as_str() {
        "M1"  => Some(Timeframe::M1),
        "M5"  => Some(Timeframe::M5),
        "M15" => Some(Timeframe::M15),
        "M30" => Some(Timeframe::M30),
        "H1"  => Some(Timeframe::H1),
        "H4"  => Some(Timeframe::H4),
        "D1"  => Some(Timeframe::D1),
        "W1"  => Some(Timeframe::W1),
        _ => None,
    }
}
