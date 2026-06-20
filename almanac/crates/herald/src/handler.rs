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
use std::time::{Duration, Instant};

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
    /// Unix-ms of the last bar event received (any TF, forming or closed).
    /// Zero until the first bar arrives. Used by the watchdog to detect feed stall.
    pub last_bar_at_ms:        AtomicU64,
}

/// Silence threshold for the bar watchdog: log a warning if no bars have been
/// received for this long. OKX/Binance typically push at least one forming bar
/// every minute, so 3 minutes means something is genuinely stuck.
const BAR_WATCHDOG_SECS: u64 = 180;

/// Max time to spend awaiting a forming-bar NATS publish.
/// Forming bars are high-frequency fire-and-forget — if NATS is slow we
/// skip rather than cascading backpressure all the way back to the WS feed.
const FORMING_BAR_PUBLISH_TIMEOUT: Duration = Duration::from_millis(200);
/// Timeout for publishing a CLOSED bar to NATS `bars.{tf}.{symbol}`.
/// Longer than forming-bar because closed bars feed downstream signals — we
/// tolerate a 2s stall before giving up and releasing the handler loop.
const CLOSED_BAR_PUBLISH_TIMEOUT: Duration = Duration::from_secs(2);

/// Threshold for warning about slow closed-bar NATS publishes.
const SLOW_PUBLISH_WARN_MS: u128 = 500;

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
        let publisher_task = tokio::spawn(signal_publisher(
            self.client.clone(),
            rx,
            Arc::clone(&self.atomics),
        ));
        tokio::pin!(publisher_task);

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

        // Watchdog: fires every BAR_WATCHDOG_SECS. If no bar has been received since
        // the last tick, the WS feed is likely stalled and we log a warning.
        let watchdog_interval = std::time::Duration::from_secs(BAR_WATCHDOG_SECS);
        let mut watchdog = tokio::time::interval(watchdog_interval);
        watchdog.tick().await; // skip the immediate first tick

        let mut watchdog_stall_count = 0u32;
        loop {
            tokio::select! {
                // bar_rx closing is the only true terminal condition: it means all WS
                // feed tasks (binance + okx) have exited. At that point herald has no
                // live data source and must restart to reconnect.
                event = self.bar_rx.recv() => match event {
                    Some(event) => self.handle_bar_event(event).await,
                    None => {
                        error!(
                            herald_id = %self.herald_id,
                            "bar feed channel closed — all WS ingesters exited; herald exiting"
                        );
                        break;
                    }
                },

                // NATS subscriptions return None when the server stalls or the
                // connection is temporarily interrupted. Previously this would
                // silently disable the arm; if all subs closed simultaneously the
                // `else` arm fired and herald exited with Ok(()).
                //
                // Fix: resubscribe immediately on None. If NATS is genuinely gone
                // the subscribe() call will return Err and propagate — giving a
                // clear error rather than a silent exit.
                msg = reset_sub.next() => match msg {
                    Some(msg) => self.handle_reset(msg).await,
                    None => {
                        warn!(subject = SUBJ_RESET, "NATS subscription closed — resubscribing");
                        reset_sub = self.client.subscribe(SUBJ_RESET).await?;
                    }
                },
                msg = register_sub.next() => match msg {
                    Some(msg) => self.handle_register(msg).await,
                    None => {
                        warn!(subject = SUBJ_REGISTER, "NATS subscription closed — resubscribing");
                        register_sub = self.client.subscribe(SUBJ_REGISTER).await?;
                    }
                },
                msg = deregister_sub.next() => match msg {
                    Some(msg) => self.handle_deregister(msg).await,
                    None => {
                        warn!(subject = SUBJ_DEREGISTER, "NATS subscription closed — resubscribing");
                        deregister_sub = self.client.subscribe(SUBJ_DEREGISTER).await?;
                    }
                },
                msg = list_sub.next() => match msg {
                    Some(msg) => self.handle_list(msg).await,
                    None => {
                        warn!(subject = SUBJ_LIST, "NATS subscription closed — resubscribing");
                        list_sub = self.client.subscribe(SUBJ_LIST).await?;
                    }
                },
                msg = ping_sub.next() => match msg {
                    Some(msg) => self.handle_ping(msg).await,
                    None => {
                        warn!(subject = SUBJ_PING, "NATS subscription closed — resubscribing");
                        ping_sub = self.client.subscribe(SUBJ_PING).await?;
                    }
                },
                msg = heartbeat_sub.next() => match msg {
                    Some(msg) => self.handle_heartbeat(msg).await,
                    None => {
                        warn!(subject = SUBJ_HEARTBEAT, "NATS subscription closed — resubscribing");
                        heartbeat_sub = self.client.subscribe(SUBJ_HEARTBEAT).await?;
                    }
                },
                msg = stats_sub.next() => match msg {
                    Some(msg) => self.handle_stats(msg).await,
                    None => {
                        warn!(subject = SUBJ_STATS, "NATS subscription closed — resubscribing");
                        stats_sub = self.client.subscribe(SUBJ_STATS).await?;
                    }
                },

                _ = watchdog.tick() => {
                    let last_ms = self.atomics.last_bar_at_ms.load(Ordering::Relaxed);
                    let now_ms = chrono::Utc::now().timestamp_millis() as u64;
                    let bars_published = self.atomics.bars_published.load(Ordering::Relaxed);
                    let nats_errs = self.atomics.nats_bars_errors.load(Ordering::Relaxed);

                    if last_ms == 0 {
                        warn!(
                            herald_id = %self.herald_id,
                            watchdog_secs = BAR_WATCHDOG_SECS,
                            "watchdog: no bars received yet since startup — WS feed may not be connected"
                        );
                    } else {
                        let elapsed_secs = now_ms.saturating_sub(last_ms) / 1000;
                        if elapsed_secs >= BAR_WATCHDOG_SECS {
                            watchdog_stall_count += 1;
                            if watchdog_stall_count >= 3 {
                                error!(
                                    herald_id = %self.herald_id,
                                    elapsed_secs,
                                    watchdog_secs = BAR_WATCHDOG_SECS,
                                    stall_ticks = watchdog_stall_count,
                                    bars_published_total = bars_published,
                                    nats_bars_errors_total = nats_errs,
                                    "watchdog: feed STALLED for 3+ consecutive ticks — WS feed likely dead"
                                );
                                counter!("herald_feed_stalled_total").increment(1);
                            } else {
                                warn!(
                                    herald_id = %self.herald_id,
                                    elapsed_secs,
                                    watchdog_secs = BAR_WATCHDOG_SECS,
                                    bars_published_total = bars_published,
                                    nats_bars_errors_total = nats_errs,
                                    "watchdog: no bars received — WS feed may be stalled or bar channel is backed up"
                                );
                            }
                        } else {
                            watchdog_stall_count = 0;
                            let hand_count = self.registry.hand_count();
                            let unique_syms = self.ledger.keys()
                                .into_iter()
                                .map(|(s, _)| s)
                                .collect::<std::collections::HashSet<_>>()
                                .len();
                            gauge!("herald_ledger_symbols").set(unique_syms as f64);
                            info!(
                                herald_id = %self.herald_id,
                                bars_published_total = bars_published,
                                nats_bars_errors_total = nats_errs,
                                secs_since_last_bar = elapsed_secs,
                                hand_count,
                                unique_syms,
                                "herald alive"
                            );
                        }
                    }

                    // Warn when NATS error rate is elevated (closed bar publish failures).
                    if nats_errs > 0 {
                        warn!(
                            herald_id = %self.herald_id,
                            nats_bars_errors_total = nats_errs,
                            "watchdog: NATS publish errors detected — check NATS connectivity"
                        );
                    }
                },

                result = &mut publisher_task => {
                    match result {
                        Ok(()) => error!(
                            herald_id = %self.herald_id,
                            "signal publisher task exited unexpectedly — all future signals will be dropped; herald shutting down"
                        ),
                        Err(e) => error!(
                            herald_id = %self.herald_id,
                            err = %e,
                            "signal publisher task panicked — herald shutting down"
                        ),
                    }
                    break;
                },
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
        self.atomics.last_bar_at_ms.store(received_at_ms as u64, Ordering::Relaxed);

        // Forming bar — update live_bar in ledger (no observer fan-out, so
        // strategies still evaluate only on closed bars). Publish per-TF to
        // bars.live.{tf}.{symbol} so the gateway/chart can tick the current candle
        // at whatever timeframe is being viewed.
        //
        // IMPORTANT: forming bars are high-frequency (multiple per second across all
        // symbols). We use a timeout instead of unbounded await to break the cascade:
        //   NATS stall → publish blocks → handler loop stalls → bar channel fills →
        //   tx.send().await in feed task blocks → WS read loop starved → OKX timeout →
        //   reconnect storm.
        // Dropping a forming-bar publish under NATS pressure is acceptable — clients
        // will see slightly stale live candles until NATS recovers.
        if !closed {
            let payload = BarMsg::from(&bar).encode_to_vec();
            let subject = format!("{}.live.{}.{}", SUBJ_BARS, tf, symbol);
            match tokio::time::timeout(
                FORMING_BAR_PUBLISH_TIMEOUT,
                self.client.publish(subject, payload.into()),
            ).await {
                Ok(Ok(())) => {}
                Ok(Err(e)) => debug!(%symbol, ?tf, err = %e, "forming bar publish failed"),
                Err(_timeout) => {
                    warn!(
                        %symbol, ?tf,
                        timeout_ms = FORMING_BAR_PUBLISH_TIMEOUT.as_millis(),
                        "forming bar NATS publish timed out — NATS is slow, skipping to avoid cascade"
                    );
                    counter!("herald_nats_publish_errors_total", "subject" => "bars.live").increment(1);
                }
            }
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
                counter!(
                    "herald_bars_confirmed_total",
                    "source" => source.to_string(),
                    "symbol" => symbol.clone(),
                    "tf"     => tf.to_string(),
                ).increment(1);
                info!(
                    %symbol, tf = %tf.to_string(), bar_ts = ts,
                    close = bar.close, latency_ms,
                    advance_us,
                    "bar confirmed"
                );
            }
            Ok(None)    => debug!(%symbol, ?tf, bar_ts = ts, closed = true, "bar skipped (dup/out-of-order)"),
            Err(e)      => error!(%symbol, ?tf, bar_ts = ts, err = %e, "ledger.advance failed"),
        }

        // Publish closed bars per-TF to bars.{tf}.{symbol} for downstream consumers
        // (gateway/chart subscribe the viewing TF). Signal generation already
        // happened via ledger.advance above (every TF), independent of this publish.
        let bar_msg = BarMsg::from(&bar);
        let payload = bar_msg.encode_to_vec();
        let subject = format!("{}.{}.{}", SUBJ_BARS, tf, symbol);
        let publish_start = Instant::now();
        match tokio::time::timeout(
            CLOSED_BAR_PUBLISH_TIMEOUT,
            self.client.publish(subject.clone(), payload.into()),
        ).await {
            Ok(Ok(())) => {
                let publish_ms = publish_start.elapsed().as_millis();
                if publish_ms > SLOW_PUBLISH_WARN_MS {
                    warn!(
                        %symbol, ?tf, publish_ms,
                        "closed bar NATS publish took too long — NATS may be backing up"
                    );
                }
                debug!(%symbol, nats_subject = %subject, "bar published to NATS");
                counter!("herald_nats_bars_published_total", "symbol" => symbol.clone()).increment(1);
                self.atomics.bars_published.fetch_add(1, Ordering::Relaxed);
            }
            Ok(Err(e)) => {
                error!(%symbol, err = %e, "failed to publish bar to NATS");
                counter!("herald_nats_publish_errors_total", "subject" => "bars").increment(1);
                self.atomics.nats_bars_errors.fetch_add(1, Ordering::Relaxed);
            }
            Err(_elapsed) => {
                warn!(
                    %symbol, ?tf,
                    timeout_ms = CLOSED_BAR_PUBLISH_TIMEOUT.as_millis(),
                    "closed bar NATS publish timed out — NATS stalled, releasing handler loop"
                );
                counter!("herald_nats_publish_errors_total", "subject" => "bars").increment(1);
                self.atomics.nats_bars_errors.fetch_add(1, Ordering::Relaxed);
            }
        }
    }

    // ── herald.stats ─────────────────────────────────────────────────────────

    async fn handle_stats(&self, msg: async_nats::Message) {
        debug!(subject = SUBJ_STATS, msg_bytes = msg.payload.len(), "handle_stats");
        let Some(reply) = msg.reply else {
            warn!("herald.stats without reply subject — ignoring");
            return;
        };

        let unique_syms = self.ledger.keys()
            .into_iter()
            .map(|(s, _)| s)
            .collect::<std::collections::HashSet<_>>()
            .len();

        let last_bar_at_ms = self.atomics.last_bar_at_ms.load(Ordering::Relaxed);
        let now_ms = chrono::Utc::now().timestamp_millis() as u64;
        let secs_since_last_bar = if last_bar_at_ms == 0 {
            None
        } else {
            Some(now_ms.saturating_sub(last_bar_at_ms) / 1000)
        };
        let data = serde_json::json!({
            "herald_id":              self.herald_id,
            "uptime_ms":              self.start_time.elapsed().as_millis() as u64,
            "ledger_symbols":         unique_syms,
            "registry_hands":         self.registry.hand_count(),
            "bars_published_total":   self.atomics.bars_published.load(Ordering::Relaxed),
            "signals_published_total":self.atomics.signals_published.load(Ordering::Relaxed),
            "nats_bars_errors_total": self.atomics.nats_bars_errors.load(Ordering::Relaxed),
            "nats_signals_errors_total": self.atomics.nats_signals_errors.load(Ordering::Relaxed),
            "last_bar_at_ms":         last_bar_at_ms,
            "secs_since_last_bar":    secs_since_last_bar,
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
        debug!(subject = SUBJ_REGISTER, msg_bytes = msg.payload.len(), "handle_register");
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
        debug!(subject = SUBJ_DEREGISTER, msg_bytes = msg.payload.len(), "handle_deregister");
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
        debug!(subject = SUBJ_LIST, msg_bytes = msg.payload.len(), "handle_list");
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
        debug!(subject = SUBJ_PING, msg_bytes = msg.payload.len(), "handle_ping");
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
            error!(
                helm_id = %batch.helm_id, hand_id = %batch.hand_id,
                symbol = %batch.signal.symbol, direction = ?batch.signal.direction,
                bar_ts = batch.bar_ts,
                "signal DROPPED after {MAX_RETRIES} publish attempts — trading bot will not receive this signal"
            );
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
