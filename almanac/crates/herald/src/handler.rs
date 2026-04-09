use crate::registry::Registry;
use async_nats::Client;
use async_nats::jetstream::consumer::{pull, Consumer};
use alm_core::msg::{
    BarMsg, BotInfo, BotListResponse, ConfigMsg, DeregisterMsg, RegisterMsg, ResetMsg, SignalMsg,
    SignalResponse,
};
use futures::StreamExt;
use prost::Message;
use std::sync::Arc;
use tokio::sync::Mutex;
use tracing::{debug, error, info, warn};

// ── NATS subjects ─────────────────────────────────────────────────────────────

pub const SUBJ_CONFIG: &str = "engine.configure";
pub const SUBJ_RESET: &str = "engine.reset";
pub const SUBJ_REGISTER: &str = "engine.register";
pub const SUBJ_DEREGISTER: &str = "engine.deregister";
pub const SUBJ_LIST: &str = "engine.list"; // request/reply

// Bars older than this are treated as warm-up only — no signals emitted.
const FRESHNESS_GATE_MS: i64 = 2 * 60 * 1000; // 2 minutes

// ── Handler ───────────────────────────────────────────────────────────────────

pub struct Handler {
    client: Client,
    consumer: Consumer<pull::Config>,
    registry: Arc<Mutex<Registry>>,
}

impl Handler {
    pub fn new(client: Client, consumer: Consumer<pull::Config>) -> Self {
        Self {
            client,
            consumer,
            registry: Arc::new(Mutex::new(Registry::default())),
        }
    }

    pub async fn run(self) -> anyhow::Result<()> {
        // JetStream durable pull consumer — durable, replays missed bars on restart.
        let mut bars_msgs = self.consumer.messages().await?;

        // Control-plane subs remain Core NATS (fire-and-forget, no replay needed).
        let mut config_sub = self.client.subscribe(SUBJ_CONFIG).await?;
        let mut reset_sub = self.client.subscribe(SUBJ_RESET).await?;
        let mut register_sub = self.client.subscribe(SUBJ_REGISTER).await?;
        let mut deregister_sub = self.client.subscribe(SUBJ_DEREGISTER).await?;
        let mut list_sub = self.client.subscribe(SUBJ_LIST).await?;

        info!(
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

        // Freshness gate: bars replayed from JetStream backfill are used for
        // warm-up only — we do not emit signals for stale data to avoid acting
        // on historical prices as if they were live.
        let now_ms = chrono::Utc::now().timestamp_millis();
        let age_ms = now_ms - bar_msg.t;
        let is_stale = age_ms > FRESHNESS_GATE_MS;

        let symbol = bar_msg.s.clone();
        let bar = alm_core::Bar::from(bar_msg);

        let results = {
            let mut reg = self.registry.lock().await;
            reg.on_bar(&bar)
        };

        // Always ack so the consumer advances — even for stale/warm-up bars.
        msg.ack().await.ok();

        if is_stale {
            debug!(%symbol, age_ms, "stale bar — warming up indicators, no signal emitted");
            return;
        }

        if results.is_empty() {
            debug!(%symbol, "bar processed, no signals");
            return;
        }

        for (bot_id, orch_id, signals) in results {
            let response = SignalResponse {
                signals: signals.iter().map(SignalMsg::from).collect(),
                orch_id: orch_id.clone(),
                bot_id: bot_id.clone(),
            };
            let payload = response.encode_to_vec();
            let subject = "signals";

            if let Err(e) = self.client.publish(subject.clone(), payload.into()).await {
                error!(%subject, err = %e, "failed to publish signals");
                continue;
            }

            for sig in &signals {
                info!(
                    bot_id = %bot_id,
                    symbol = %sig.symbol,
                    direction = ?sig.direction,
                    strength = sig.strength,
                    "signal emitted"
                );
            }
        }
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
        let mut reg = self.registry.lock().await;
        reg.set_global_config(config.strategy, params_json);
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

        let mut reg = self.registry.lock().await;
        reg.reset(symbol);
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

        let result = {
            let mut reg = self.registry.lock().await;
            reg.register(
                req.bot_id.clone(),
                req.orch_id.clone(),
                req.symbol.clone(),
                req.strategy.clone(),
                req.params_json.clone(),
            )
        };

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
                    let _ = self
                        .client
                        .publish(reply, format!("error: {e}").into())
                        .await;
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

        let mut reg = self.registry.lock().await;
        reg.deregister(bot_id);

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

        let bots = {
            let reg = self.registry.lock().await;
            reg.list_bots()
                .into_iter()
                .map(|(bot_id, _orch_id, symbol, strategy, params_json)| BotInfo {
                    bot_id: bot_id.to_string(),
                    symbol: symbol.to_string(),
                    strategy: strategy.to_string(),
                    params_json: params_json.to_string(),
                })
                .collect()
        };

        let response = BotListResponse { bots };
        let payload = response.encode_to_vec();
        let _ = self.client.publish(reply, payload.into()).await;
    }
}
