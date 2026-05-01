//! Bar ingestion + NATS control plane.
//!
//! Architecture (WebSocket-native):
//!
//! ```text
//!   feed::binance  ─┐
//!   feed::okx      ─┤  mpsc::UnboundedSender<Bar>
//!                   ▼
//!              Handler::run
//!                   │
//!          ┌────────┼───────────────┐
//!          ▼        ▼               ▼
//!       BarRing  Ledger::advance  NATS publish bars.{symbol}
//!                   │
//!                   ▼ fan-out
//!            LedgerObserver(s)
//!                   │
//!             Registry (mpsc)
//!                   │
//!                   ▼
//!          signal_publisher → NATS "signals"
//! ```

use std::sync::Arc;

use alm_core::msg::{
    BarMsg, BotInfo, BotListResponse, ConfigMsg, DeregisterMsg, RegisterMsg, ResetMsg, SignalMsg,
    SignalResponse,
};
use alm_core::{Bar, Timeframe};
use alm_ledger::Ledger;
use async_nats::Client;
use futures::StreamExt;
use prost::Message as _;
use tokio::sync::{broadcast, mpsc};
use tracing::{debug, error, info, warn};

use crate::feed::BarRx;
use crate::registry::{Registry, SignalBatch};
use crate::ring::BarRing;

// ── NATS subjects ─────────────────────────────────────────────────────────────

pub const SUBJ_CONFIG: &str = "engine.configure";
pub const SUBJ_RESET: &str = "engine.reset";
pub const SUBJ_REGISTER: &str = "engine.register";
pub const SUBJ_DEREGISTER: &str = "engine.deregister";
pub const SUBJ_LIST: &str = "engine.list";
pub const SUBJ_SIGNALS: &str = "signals";
pub const SUBJ_BARS: &str = "bars";

// ── Handler ───────────────────────────────────────────────────────────────────

pub struct Handler {
    client: Client,
    ledger: Arc<Ledger>,
    registry: Arc<Registry>,
    ring: BarRing,
    tf: Timeframe,
    bar_rx: BarRx,
    signal_rx: Option<mpsc::UnboundedReceiver<SignalBatch>>,
    bar_bcast: broadcast::Sender<Bar>,
    sig_bcast: broadcast::Sender<Arc<SignalBatch>>,
}

impl Handler {
    pub fn new(
        client: Client,
        ledger: Arc<Ledger>,
        registry: Arc<Registry>,
        ring: BarRing,
        tf: Timeframe,
        bar_rx: BarRx,
        signal_rx: mpsc::UnboundedReceiver<SignalBatch>,
        bar_bcast: broadcast::Sender<Bar>,
        sig_bcast: broadcast::Sender<Arc<SignalBatch>>,
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
        }
    }

    pub async fn run(mut self) -> anyhow::Result<()> {
        let rx = self.signal_rx.take().expect("signal_rx present at run()");
        tokio::spawn(signal_publisher(self.client.clone(), rx, self.sig_bcast.clone()));

        let mut config_sub     = self.client.subscribe(SUBJ_CONFIG).await?;
        let mut reset_sub      = self.client.subscribe(SUBJ_RESET).await?;
        let mut register_sub   = self.client.subscribe(SUBJ_REGISTER).await?;
        let mut deregister_sub = self.client.subscribe(SUBJ_DEREGISTER).await?;
        let mut list_sub       = self.client.subscribe(SUBJ_LIST).await?;

        info!(
            tf = ?self.tf,
            "herald ready (WebSocket ingestion mode)"
        );

        loop {
            tokio::select! {
                Some(bar) = self.bar_rx.recv() => self.handle_bar(bar).await,
                Some(msg) = config_sub.next()      => self.handle_config(msg).await,
                Some(msg) = reset_sub.next()       => self.handle_reset(msg).await,
                Some(msg) = register_sub.next()    => self.handle_register(msg).await,
                Some(msg) = deregister_sub.next()  => self.handle_deregister(msg).await,
                Some(msg) = list_sub.next()        => self.handle_list(msg).await,
                else => break,
            }
        }
        Ok(())
    }

    // ── bar ───────────────────────────────────────────────────────────────────

    async fn handle_bar(&self, bar: Bar) {
        let symbol = bar.symbol.clone();
        let ts = bar.timestamp;

        // 1. Push to 24h ring buffer.
        self.ring.push(bar.clone());

        // 2. Advance ledger (drives indicator state + signals via observer).
        match self.ledger.advance(self.tf, bar.clone()) {
            Ok(Some(_)) => debug!(%symbol, bar_ts = ts, "bar advanced"),
            Ok(None)    => debug!(%symbol, bar_ts = ts, "bar skipped (dup/out-of-order)"),
            Err(e)      => error!(%symbol, bar_ts = ts, err = %e, "ledger.advance failed"),
        }

        // 3. Broadcast to SSE subscribers (lagged receivers are silently dropped).
        let _ = self.bar_bcast.send(bar.clone());

        // 4. Publish to NATS bars.{symbol} for downstream consumers.
        let bar_msg = BarMsg::from(&bar);
        let payload = bar_msg.encode_to_vec();
        let subject = format!("{}.{}", SUBJ_BARS, symbol);
        if let Err(e) = self.client.publish(subject, payload.into()).await {
            error!(%symbol, err = %e, "failed to publish bar to NATS");
        }
    }

    // ── engine.configure ─────────────────────────────────────────────────────

    async fn handle_config(&self, msg: async_nats::Message) {
        let config = match ConfigMsg::decode(msg.payload.as_ref()) {
            Ok(c) => c,
            Err(e) => { error!(err = %e, "failed to decode ConfigMsg"); return; }
        };
        let params_json = serde_json::to_string(&config.params).unwrap_or_default();
        info!(strategy = %config.strategy, "reconfiguring global strategy (legacy)");
        self.registry.set_global_config(config.strategy, params_json);
    }

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
        info!(bot_id = %req.bot_id, symbol = %req.symbol, strategy = %req.strategy, "registering bot");
        let target_tf = req.timeframe.as_deref()
            .and_then(parse_timeframe_str)
            .unwrap_or(self.tf);
        let result = self.registry.register(
            req.bot_id.clone(), req.orch_id.clone(), req.symbol.clone(),
            req.strategy.clone(), req.params_json.clone(), target_tf,
        );
        match result {
            Ok(()) => {
                info!(bot_id = %req.bot_id, symbol = %req.symbol, "bot registered");
                if let Some(reply) = msg.reply {
                    let _ = self.client.publish(reply, b"ok".as_ref().into()).await;
                }
            }
            Err(e) => {
                warn!(bot_id = %req.bot_id, err = %e, "failed to register bot");
                if let Some(reply) = msg.reply {
                    let _ = self.client.publish(reply, format!("error: {e}").into()).await;
                }
            }
        }
    }

    // ── engine.deregister ────────────────────────────────────────────────────

    async fn handle_deregister(&self, msg: async_nats::Message) {
        let req = match DeregisterMsg::decode(msg.payload.as_ref()) {
            Ok(r) => r,
            Err(e) => { error!(err = %e, "failed to decode DeregisterMsg"); return; }
        };
        let bot_id = req.bot_id.as_str();
        if bot_id.is_empty() { info!("deregistering ALL bots"); }
        else                  { info!(%bot_id, "deregistering bot"); }
        self.registry.deregister(bot_id);
        if let Some(reply) = msg.reply {
            let _ = self.client.publish(reply, b"ok".as_ref().into()).await;
        }
    }

    // ── engine.list ──────────────────────────────────────────────────────────

    async fn handle_list(&self, msg: async_nats::Message) {
        let Some(reply) = msg.reply else {
            warn!("engine.list without reply subject — ignoring");
            return;
        };
        let bots = self.registry.list_bots().into_iter()
            .map(|(bot_id, _orch_id, symbol, strategy, params_json)| BotInfo {
                bot_id, symbol, strategy, params_json,
            })
            .collect();
        let payload = BotListResponse { bots }.encode_to_vec();
        let _ = self.client.publish(reply, payload.into()).await;
    }
}

// ── Signal publisher task ─────────────────────────────────────────────────────

async fn signal_publisher(
    client: Client,
    mut rx: mpsc::UnboundedReceiver<SignalBatch>,
    bcast: broadcast::Sender<Arc<SignalBatch>>,
) {
    while let Some(batch) = rx.recv().await {
        let batch = Arc::new(batch);

        // Broadcast to SSE subscribers before consuming the batch.
        let _ = bcast.send(Arc::clone(&batch));

        let response = SignalResponse {
            signals: batch.signals.iter().map(SignalMsg::from).collect(),
            orch_id: batch.orch_id.clone(),
            bot_id: batch.bot_id.clone(),
        };
        let payload = response.encode_to_vec();
        if let Err(e) = client.publish(SUBJ_SIGNALS, payload.into()).await {
            error!(subject = SUBJ_SIGNALS, err = %e, "failed to publish signals");
            continue;
        }
        for sig in &batch.signals {
            info!(
                bot_id = %batch.bot_id, symbol = %sig.symbol,
                direction = ?sig.direction, strength = sig.strength,
                bar_ts = batch.bar_ts, "signal emitted"
            );
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
