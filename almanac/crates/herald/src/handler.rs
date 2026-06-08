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
//!          closed? ─┼───────────────────────────────────────┐
//!          yes      │                                       │ no (forming)
//!                   ▼                                       ▼
//!          Ledger::advance(tf, bar)             Ledger::advance_live(tf, bar)
//!                   │                           (no observer notification)
//!                   ▼ fan-out (base TF only)
//!            LedgerObserver(s)
//!                   │
//!             Registry (mpsc)  ──→ signal_publisher → NATS "signals"
//!                   │
//!           (base TF closed) ──→ BarRing + NATS bars.{symbol}
//! ```

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Instant;

use metrics::{counter, gauge, histogram};

// build_strategy / probe_script_htfs / MtfScriptStrategy aren't needed here:
// `Registry::register` runs the real build internally and returns its error
// directly, so we don't pre-validate in this handler anymore.
use alm_core::msg::{
    BarMsg, HandInfo, HandListResponse, DeregisterMsg, HeartbeatRequest, HeartbeatResponse,
    PingResponse, ReadyEvent, RegisterMsg, ResetMsg, SignalMsg, SignalResponse,
};
use alm_core::Timeframe;
use alm_ledger::Ledger;
use async_nats::{Client, jetstream};
use futures::StreamExt;
use prost::Message as _;
use tokio::sync::mpsc;
use tracing::{debug, error, info, warn};
use uuid::Uuid;

use alm_herald::WsLatencyTracker;
use crate::feed::{BarEvent, BarRx};
use crate::registry::{HandSignal, Registry};

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
/// NATS req/reply subject for runtime stats — consumed by strategist AI.
pub const SUBJ_STATS: &str = "herald.stats";

// ── Shared atomic counters ────────────────────────────────────────────────────

/// Atomic counters shared between `Handler` and the `signal_publisher` task.
/// These back the `herald.stats` NATS endpoint without requiring a Prometheus
/// text parse. Values are also mirrored to the `metrics` registry.
#[derive(Default)]
pub struct HeraldAtomics {
    pub bars_published:        AtomicU64,
    pub signals_published:     AtomicU64,
    pub nats_bars_errors:      AtomicU64,
    pub nats_signals_errors:   AtomicU64,
}

// ── Handler ───────────────────────────────────────────────────────────────────

pub struct Handler {
    client: Client,
    ledger: Arc<Ledger>,
    registry: Arc<Registry>,
    /// Base timeframe — only closed bars at this TF trigger NATS publish +
    /// Registry observer fan-out.
    tf: Timeframe,
    bar_rx: BarRx,
    signal_rx: Option<mpsc::Receiver<HandSignal>>,
    /// Stable per-instance identifier — changes on every restart so consumers
    /// can detect herald restart and re-register their hands.
    herald_id: String,
    start_time: Instant,
    /// WebSocket delivery latency tracker — shared with the HTTP admin endpoint.
    ws_latency: Arc<WsLatencyTracker>,
    /// Shared counters for the herald.stats NATS endpoint.
    atomics: Arc<HeraldAtomics>,
}

impl Handler {
    pub fn new(
        client: Client,
        ledger: Arc<Ledger>,
        registry: Arc<Registry>,
        tf: Timeframe,
        bar_rx: BarRx,
        signal_rx: mpsc::Receiver<HandSignal>,
        ws_latency: Arc<WsLatencyTracker>,
    ) -> Self {
        Self {
            client,
            ledger,
            registry,
            tf,
            bar_rx,
            signal_rx: Some(signal_rx),
            herald_id: Uuid::now_v7().to_string(),
            start_time: Instant::now(),
            ws_latency,
            atomics: Arc::new(HeraldAtomics::default()),
        }
    }

    pub async fn run(mut self) -> anyhow::Result<()> {
        let rx = self.signal_rx.take().expect("signal_rx present at run()");
        tokio::spawn(signal_publisher(
            self.client.clone(),
            rx,
            Arc::clone(&self.atomics),
        ));

        let mut reset_sub      = self.client.subscribe(SUBJ_RESET).await?;
        let mut register_sub   = self.client.subscribe(SUBJ_REGISTER).await?;
        let mut deregister_sub = self.client.subscribe(SUBJ_DEREGISTER).await?;
        let mut list_sub       = self.client.subscribe(SUBJ_LIST).await?;
        let mut ping_sub       = self.client.subscribe(SUBJ_PING).await?;
        let mut heartbeat_sub  = self.client.subscribe(SUBJ_HEARTBEAT).await?;
        let mut stats_sub      = self.client.subscribe(SUBJ_STATS).await?;

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
                Some(msg) = stats_sub.next()            => self.handle_stats(msg).await,
                else => break,
            }
        }
        Ok(())
    }

    async fn publish_ready(&self) {
        let mut symbols: Vec<String> = self.registry.list_hands()
            .into_iter()
            .map(|h| h.symbol)
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
        let BarEvent { tf, bar, closed, received_at_ms } = event;
        let symbol = bar.symbol.clone();
        let ts = bar.timestamp;

        // Forming bar — update live_bar in ledger only, no observer fan-out.
        if !closed {
            self.ledger.advance_live(tf, bar);
            return;
        }

        // Record WS delivery latency: time from bar close to message receipt.
        // close_time_ms = open_time + tf duration. Positive = normal delivery lag.
        let close_time_ms = bar.timestamp + tf.duration_ms();
        let latency_ms    = received_at_ms - close_time_ms;
        let source = symbol.split(':').next().unwrap_or("unknown");
        self.ws_latency.record(source, latency_ms);

        // Confirmed bar — advance the ledger slice for this TF.
        let advance_start = Instant::now();
        let advance_result = self.ledger.advance(tf, bar.clone());
        let advance_us = advance_start.elapsed().as_micros() as f64;
        histogram!("herald_ledger_advance_us", "symbol" => symbol.clone(), "tf" => tf.to_string())
            .record(advance_us);

        match advance_result {
            Ok(Some(_)) => {
                let unique_syms = self.ledger.keys()
                    .into_iter()
                    .map(|(s, _)| s)
                    .collect::<std::collections::HashSet<_>>()
                    .len();
                gauge!("herald_ledger_symbols").set(unique_syms as f64);
                debug!(
                    %symbol, ?tf, bar_ts = ts,
                    open = bar.open, high = bar.high, low = bar.low,
                    close = bar.close, vol = bar.volume,
                    "bar confirmed"
                );
            }
            Ok(None)    => debug!(%symbol, ?tf, bar_ts = ts, "bar skipped (dup/out-of-order)"),
            Err(e)      => error!(%symbol, ?tf, bar_ts = ts, err = %e, "ledger.advance failed"),
        }

        // Base TF only: push to ring, NATS publish.
        if tf != self.tf {
            return;
        }

        // Publish to NATS bars.{symbol} for downstream consumers.
        let bar_msg = BarMsg::from(&bar);
        let payload = bar_msg.encode_to_vec();
        let subject = format!("{}.{}", SUBJ_BARS, symbol);
        if let Err(e) = self.client.publish(subject.clone(), payload.into()).await {
            error!(%symbol, err = %e, "failed to publish bar to NATS");
            counter!("herald_nats_publish_errors_total", "subject" => "bars").increment(1);
            self.atomics.nats_bars_errors.fetch_add(1, Ordering::Relaxed);
        } else {
            debug!(%symbol, nats_subject = %subject, "bar published to NATS");
            counter!("herald_nats_bars_published_total", "symbol" => symbol.clone()).increment(1);
            self.atomics.bars_published.fetch_add(1, Ordering::Relaxed);
        }
    }

    // ── herald.stats ─────────────────────────────────────────────────────────

    async fn handle_stats(&self, msg: async_nats::Message) {
        let Some(reply) = msg.reply else {
            warn!("herald.stats without reply subject — ignoring");
            return;
        };

        let unique_syms = self.ledger.keys()
            .into_iter()
            .map(|(s, _)| s)
            .collect::<std::collections::HashSet<_>>()
            .len();

        let data = serde_json::json!({
            "herald_id":              self.herald_id,
            "uptime_ms":              self.start_time.elapsed().as_millis() as u64,
            "ledger_symbols":         unique_syms,
            "registry_hands":         self.registry.hand_count(),
            "bars_published_total":   self.atomics.bars_published.load(Ordering::Relaxed),
            "signals_published_total":self.atomics.signals_published.load(Ordering::Relaxed),
            "nats_bars_errors_total": self.atomics.nats_bars_errors.load(Ordering::Relaxed),
            "nats_signals_errors_total": self.atomics.nats_signals_errors.load(Ordering::Relaxed),
        });
        let resp = serde_json::json!({"ok": true, "data": data}).to_string();
        let _ = self.client.publish(reply, resp.into_bytes().into()).await;
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
        // Build the ledger key: "{exchange}:{symbol}" when exchange is set,
        // plain symbol otherwise (tests / backtest-only setups).
        let ledger_key = if req.exchange.is_empty() {
            req.symbol.clone()
        } else {
            format!("{}:{}", req.exchange, req.symbol)
        };
        info!(hand_id = %req.hand_id, symbol = %ledger_key, "registering hand");
        // Timeframe is required — strategies are TF-specific so silent fallback
        // to herald's base TF would run a script designed for H1 on M1 bars.
        let target_tf = match req.timeframe.as_str() {
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
                        "invalid timeframe `{s}` (supported: M1, M3, M5, M15, M30, H1, H2, H4, H6, H12, D1, W1, MN)"
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
        // No separate validation pass: `Registry::register` builds the real
        // strategy via `Handle::new`, which runs the same Rhai compile +
        // indicator instantiation that any dry-run probe would do. Errors
        // surface here as `Err(_)` and are returned in the ack. Avoids the
        // double-compile we used to do (probe-then-discard + register build).
        let result = self.registry.register(
            req.hand_id.clone(), req.helm_id.clone(), ledger_key.clone(),
            req.exchange.clone(), req.is_future,
            req.script.clone(), target_tf,
        );
        if let Some(reply) = msg.reply {
            let ack = match result {
                Ok(()) => {
                    info!(hand_id = %req.hand_id, symbol = %ledger_key, "hand registered");
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
            .map(|h| HandInfo {
                hand_id:   h.hand_id,
                helm_id:   h.helm_id,
                symbol:    h.symbol,
                script:    h.script,
                timeframe: h.timeframe,
                exchange:  h.exchange,
                is_future: h.is_future,
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

        // (hand_id, helm_id) tuples currently in our registry, scoped to this
        // helm so we don't report hands owned by sibling helms as orphan.
        //
        // Fallback / default hands are seeded by herald itself with empty
        // helm_id (see `Registry::ensure_fallback_hand`) — they must NOT be
        // reported as orphan to any caller, since no helm owns them.
        let all_hands = self.registry.list_hands();
        let our_hands: std::collections::HashSet<&str> = all_hands
            .iter()
            .filter(|h| !h.helm_id.is_empty() && h.helm_id == req.helm_id)
            .map(|h| h.hand_id.as_str())
            .collect();
        let expected: std::collections::HashSet<&str> =
            req.hands.iter().map(String::as_str).collect();

        // `missing`    = helm expects them, we don't have them → helm must register.
        // `registered` = helm expects them, we have them → confirm.
        // `orphan`     = we have them under helm_id, helm doesn't expect them → helm must deregister.
        let mut missing = Vec::new();
        let mut registered = Vec::new();
        for hand_id in &req.hands {
            if our_hands.contains(hand_id.as_str()) {
                registered.push(hand_id.clone());
            } else {
                missing.push(hand_id.clone());
            }
        }
        let orphan: Vec<String> = our_hands
            .iter()
            .filter(|id| !expected.contains(**id))
            .map(|s| s.to_string())
            .collect();

        if !missing.is_empty() {
            warn!(helm_id = %req.helm_id, missing = ?missing, "heartbeat: hands missing from registry");
        }
        if !orphan.is_empty() {
            warn!(helm_id = %req.helm_id, orphan = ?orphan, "heartbeat: orphan hands in registry");
        }
        let ok = missing.is_empty() && orphan.is_empty();
        let resp = HeartbeatResponse { ok, missing, registered, orphan };
        let _ = self.client.publish(reply, resp.encode_to_vec().into()).await;
    }
}

// ── Signal publisher task ─────────────────────────────────────────────────────

async fn signal_publisher(
    client: Client,
    mut rx: mpsc::Receiver<HandSignal>,
    atomics: Arc<HeraldAtomics>,
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

    const MAX_RETRIES: usize = 3;
    const RETRY_DELAY: std::time::Duration = std::time::Duration::from_millis(200);

    while let Some(batch) = rx.recv().await {
        let response = SignalResponse {
            signal: Some(SignalMsg::from(&batch.signal)),
            helm_id: batch.helm_id.clone(),
            hand_id: batch.hand_id.clone(),
        };
        let payload: Vec<u8> = response.encode_to_vec();
        debug!(
            helm_id = %batch.helm_id, hand_id = %batch.hand_id,
            symbol = %batch.signal.symbol, subject = SUBJ_SIGNALS,
            "publishing signal to NATS"
        );

        let mut published = false;
        for attempt in 0..MAX_RETRIES {
            match js.publish(SUBJ_SIGNALS, payload.clone().into()).await {
                Ok(ack_future) => {
                    match ack_future.await {
                        Ok(_) => {
                            info!(
                                helm_id = %batch.helm_id, hand_id = %batch.hand_id,
                                symbol = %batch.signal.symbol, subject = SUBJ_SIGNALS,
                                direction = ?batch.signal.direction, strength = batch.signal.strength,
                                bar_ts = batch.bar_ts,
                                "signal published to NATS"
                            );
                            counter!("herald_nats_signals_published_total").increment(1);
                            atomics.signals_published.fetch_add(1, Ordering::Relaxed);
                            published = true;
                            break;
                        }
                        Err(e) if attempt + 1 < MAX_RETRIES => {
                            warn!(attempt = attempt + 1, err = %e, "signal ack failed — retrying");
                            tokio::time::sleep(RETRY_DELAY).await;
                        }
                        Err(e) => {
                            error!(subject = SUBJ_SIGNALS, err = %e, "JetStream signal ack failed after {MAX_RETRIES} attempts");
                        }
                    }
                }
                Err(e) if attempt + 1 < MAX_RETRIES => {
                    warn!(attempt = attempt + 1, err = %e, "signal publish failed — retrying");
                    tokio::time::sleep(RETRY_DELAY).await;
                }
                Err(e) => {
                    error!(subject = SUBJ_SIGNALS, err = %e, "JetStream signal publish failed after {MAX_RETRIES} attempts");
                }
            }
        }
        if !published {
            counter!("herald_nats_publish_errors_total", "subject" => "signals").increment(1);
            atomics.nats_signals_errors.fetch_add(1, Ordering::Relaxed);
        }
    }
    info!("signal publisher channel closed");
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn parse_timeframe_str(s: &str) -> Option<Timeframe> {
    crate::feed::parse_tf(s)
}
