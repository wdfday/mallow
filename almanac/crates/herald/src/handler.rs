//! Bar ingestion + NATS control plane.
//!
//! Architecture (post-ledger):
//!
//! ```text
//!   JetStream BARS      ┌─────────────────────┐
//!     bars.{symbol}  ──▶│  Handler::handle_bar│──┐
//!                       └──────────┬──────────┘  │ (sync advance)
//!                                  ▼             │
//!                              Ledger::advance   │
//!                                  │             │
//!                                  ▼ fan-out     │
//!                            LedgerObserver(s)   │
//!                                  │             │
//!                    ┌─────────────┼──────────┐  │
//!                    ▼             ▼          ▼  │
//!                Registry      (future WS)  (...)│
//!                    │                            │
//!                    ▼ mpsc                       │
//!                 SignalBatch                     │
//!                    │                            │
//!                    ▼                            │
//!             Handler::publisher  (async task)    │
//!                    │             ack ◀──────────┘
//!                    ▼
//!                NATS "signals"
//! ```
//!
//! Freshness gate (2 min) is applied inside the Registry observer because
//! replayed JetStream bars drive ledger + indicator warm-up but must not
//! trigger live signals.

use std::sync::Arc;

use async_nats::jetstream::consumer::{pull, Consumer};
use async_nats::Client;
use alm_core::msg::{
    BarMsg, BotInfo, BotListResponse, ConfigMsg, DeregisterMsg, RegisterMsg, ResetMsg, SignalMsg,
    SignalResponse,
};
use alm_core::Timeframe;
use alm_ledger::Ledger;
use futures::StreamExt;
use prost::Message;
use tokio::sync::mpsc;
use tracing::{debug, error, info, warn};

use crate::registry::{Registry, SignalBatch};

// ── NATS subjects ─────────────────────────────────────────────────────────────

pub const SUBJ_CONFIG: &str = "engine.configure";
pub const SUBJ_RESET: &str = "engine.reset";
pub const SUBJ_REGISTER: &str = "engine.register";
pub const SUBJ_DEREGISTER: &str = "engine.deregister";
pub const SUBJ_LIST: &str = "engine.list"; // request/reply
pub const SUBJ_SIGNALS: &str = "signals";

// ── Handler ───────────────────────────────────────────────────────────────────

pub struct Handler {
    client: Client,
    consumer: Consumer<pull::Config>,
    ledger: Arc<Ledger>,
    registry: Arc<Registry>,
    tf: Timeframe,
    /// Owned by Handler during construction; moved out to the publisher task
    /// when `run()` starts, so `handle_bar` never touches it.
    signal_rx: Option<mpsc::UnboundedReceiver<SignalBatch>>,
}

impl Handler {
    pub fn new(
        client: Client,
        consumer: Consumer<pull::Config>,
        ledger: Arc<Ledger>,
        registry: Arc<Registry>,
        tf: Timeframe,
        signal_rx: mpsc::UnboundedReceiver<SignalBatch>,
    ) -> Self {
        Self {
            client,
            consumer,
            ledger,
            registry,
            tf,
            signal_rx: Some(signal_rx),
        }
    }

    pub async fn run(mut self) -> anyhow::Result<()> {
        // Spawn the signal publisher task first so we don't drop any signals
        // produced during the very first bar.
        let rx = self.signal_rx.take().expect("signal_rx must be present at run()");
        tokio::spawn(signal_publisher(self.client.clone(), rx));

        // JetStream durable pull consumer — replays missed bars on restart.
        let mut bars_msgs = self.consumer.messages().await?;

        // Control-plane subs remain Core NATS (fire-and-forget).
        let mut config_sub = self.client.subscribe(SUBJ_CONFIG).await?;
        let mut reset_sub = self.client.subscribe(SUBJ_RESET).await?;
        let mut register_sub = self.client.subscribe(SUBJ_REGISTER).await?;
        let mut deregister_sub = self.client.subscribe(SUBJ_DEREGISTER).await?;
        let mut list_sub = self.client.subscribe(SUBJ_LIST).await?;

        info!(
            tf = ?self.tf,
            config = SUBJ_CONFIG,
            reset = SUBJ_RESET,
            register = SUBJ_REGISTER,
            deregister = SUBJ_DEREGISTER,
            list = SUBJ_LIST,
            "herald subscribed (bars via JetStream pull consumer 'herald')"
        );

        loop {
            tokio::select! {
                Some(result) = bars_msgs.next() => {
                    match result {
                        Ok(msg) => self.handle_bar(msg).await,
                        Err(e) => error!(err = %e, "JetStream bars stream error"),
                    }
                }
                Some(msg) = config_sub.next()     => self.handle_config(msg).await,
                Some(msg) = reset_sub.next()      => self.handle_reset(msg).await,
                Some(msg) = register_sub.next()   => self.handle_register(msg).await,
                Some(msg) = deregister_sub.next() => self.handle_deregister(msg).await,
                Some(msg) = list_sub.next()       => self.handle_list(msg).await,
                else => break,
            }
        }
        Ok(())
    }

    // ── bar (JetStream) ───────────────────────────────────────────────────────

    async fn handle_bar(&self, msg: async_nats::jetstream::Message) {
        let bar_msg = match BarMsg::decode(msg.payload.as_ref()) {
            Ok(b) => b,
            Err(e) => {
                error!(subject = %msg.subject, err = %e, "failed to decode BarMsg");
                msg.ack().await.ok();
                return;
            }
        };

        let symbol = bar_msg.s.clone();
        let ts = bar_msg.t;
        let bar = alm_core::Bar::from(bar_msg);

        // Advance the ledger — this synchronously updates state, runs
        // every registered indicator, and fans out to observers (Registry).
        // Observers push SignalBatch through the mpsc channel which the
        // publisher task drains asynchronously.
        match self.ledger.advance(self.tf, bar) {
            Ok(Some(_outcome)) => {
                debug!(%symbol, bar_ts = ts, "bar advanced");
            }
            Ok(None) => {
                debug!(%symbol, bar_ts = ts, "bar skipped (out-of-order / duplicate)");
            }
            Err(e) => {
                error!(%symbol, bar_ts = ts, err = %e, "ledger.advance failed");
            }
        }

        // Always ack so JetStream advances — even stale / skipped bars.
        msg.ack().await.ok();
    }

    // ── engine.configure (legacy — global config) ─────────────────────────────

    async fn handle_config(&self, msg: async_nats::Message) {
        let config = match ConfigMsg::decode(msg.payload.as_ref()) {
            Ok(c) => c,
            Err(e) => {
                error!(err = %e, "failed to decode ConfigMsg");
                return;
            }
        };

        let params_json = serde_json::to_string(&config.params).unwrap_or_default();

        info!(strategy = %config.strategy, "reconfiguring global strategy (legacy)");
        self.registry.set_global_config(config.strategy, params_json);
    }

    // ── engine.reset ──────────────────────────────────────────────────────────

    async fn handle_reset(&self, msg: async_nats::Message) {
        let reset = match ResetMsg::decode(msg.payload.as_ref()) {
            Ok(r) => r,
            Err(e) => {
                error!(err = %e, "failed to decode ResetMsg");
                return;
            }
        };

        let symbol = reset.symbol.as_str();
        if symbol.is_empty() {
            info!("resetting all symbols");
        } else {
            info!(%symbol, "resetting symbol");
        }

        self.registry.reset(symbol);
    }

    // ── engine.register ───────────────────────────────────────────────────────

    async fn handle_register(&self, msg: async_nats::Message) {
        let req = match RegisterMsg::decode(msg.payload.as_ref()) {
            Ok(r) => r,
            Err(e) => {
                error!(err = %e, "failed to decode RegisterMsg");
                return;
            }
        };

        info!(
            bot_id = %req.bot_id,
            symbol = %req.symbol,
            strategy = %req.strategy,
            "registering bot"
        );

        let result = self.registry.register(
            req.bot_id.clone(),
            req.orch_id.clone(),
            req.symbol.clone(),
            req.strategy.clone(),
            req.params_json.clone(),
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

    // ── engine.deregister ─────────────────────────────────────────────────────

    async fn handle_deregister(&self, msg: async_nats::Message) {
        let req = match DeregisterMsg::decode(msg.payload.as_ref()) {
            Ok(r) => r,
            Err(e) => {
                error!(err = %e, "failed to decode DeregisterMsg");
                return;
            }
        };

        let bot_id = req.bot_id.as_str();
        if bot_id.is_empty() {
            info!("deregistering ALL bots");
        } else {
            info!(%bot_id, "deregistering bot");
        }

        self.registry.deregister(bot_id);

        if let Some(reply) = msg.reply {
            let _ = self.client.publish(reply, b"ok".as_ref().into()).await;
        }
    }

    // ── engine.list (request/reply) ───────────────────────────────────────────

    async fn handle_list(&self, msg: async_nats::Message) {
        let reply = match msg.reply {
            Some(r) => r,
            None => {
                warn!("engine.list received without reply subject — ignoring");
                return;
            }
        };

        let bots = self
            .registry
            .list_bots()
            .into_iter()
            .map(|(bot_id, _orch_id, symbol, strategy, params_json)| BotInfo {
                bot_id,
                symbol,
                strategy,
                params_json,
            })
            .collect();

        let response = BotListResponse { bots };
        let payload = response.encode_to_vec();
        let _ = self.client.publish(reply, payload.into()).await;
    }
}

// ── Signal publisher task ─────────────────────────────────────────────────────

/// Drains `SignalBatch` from the Registry mpsc channel and publishes each
/// batch to NATS subject `signals`. Runs for the lifetime of Handler::run.
async fn signal_publisher(client: Client, mut rx: mpsc::UnboundedReceiver<SignalBatch>) {
    while let Some(batch) = rx.recv().await {
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
                bot_id = %batch.bot_id,
                symbol = %sig.symbol,
                direction = ?sig.direction,
                strength = sig.strength,
                bar_ts = batch.bar_ts,
                "signal emitted"
            );
        }
    }
    info!("signal publisher channel closed — exiting");
}
