//! Watch signal evaluator — `LedgerObserver` that runs strategy logic for
//! each `WatchEntry` and dispatches signals to webhook or NATS.
//!
//! Separated from the bot `Registry` so that watch signals never touch the
//! trade-execution path.
//!
//! ## Lifecycle
//!
//! - Created once at startup alongside `Registry`.
//! - `WatchStore` is read on every advance (`try_read` — skips if a write is
//!   in flight from an HTTP handler).
//! - Strategy handles are created lazily on first matching bar and cached;
//!   `remove_watch` drops them explicitly when an entry is deleted via HTTP.
//! - Signals older than `FRESHNESS_GATE_MS` are suppressed (warm-up bars must
//!   not trigger live notifications).

use std::collections::HashMap;
use std::sync::Arc;

use alm_core::{signal::Signal, strategy::Strategy, Bar, Timeframe};
use alm_ledger::{AdvanceOutcome, IndicatorHandle, IndicatorSpec, Ledger, LedgerObserver};
use alm_strategy::factory::build_strategy_with_deps;
use parking_lot::Mutex;
use serde::Serialize;
use tokio::sync::mpsc;
use tracing::{debug, trace, warn};

use crate::http::store::types::StrategySpec;
use crate::http::watch::WatchStore;
use crate::registry::FRESHNESS_GATE_MS;

// ── WatchSignalBatch ──────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize)]
pub struct WatchSignalBatch {
    pub watch_id:     String,
    pub symbol:       String,
    pub bar_ts:       i64,
    pub signals:      Vec<WatchSignal>,
    #[serde(skip)]
    pub webhook_url:  Option<String>,
    #[serde(skip)]
    pub nats_subject: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
pub struct WatchSignal {
    pub direction: String,
    pub strength:  f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub price:     Option<f64>,
}

impl From<&Signal> for WatchSignal {
    fn from(s: &Signal) -> Self {
        Self {
            direction: format!("{:?}", s.direction).to_lowercase(),
            strength:  s.strength,
            price:     s.price,
        }
    }
}

// ── WatchHandle ───────────────────────────────────────────────────────────────

struct WatchHandle {
    strategy:           Box<dyn Strategy>,
    _indicator_handles: Vec<IndicatorHandle>,
}

// ── Data collected from WatchStore in one read pass ──────────────────────────

struct MatchedWatch {
    id:           String,
    spec:         StrategySpec,
    webhook_url:  Option<String>,
    nats_subject: Option<String>,
}

// ── WatchEvaluator ────────────────────────────────────────────────────────────

pub struct WatchEvaluator {
    /// Cached per-(watch_id, symbol) strategy state.
    handles:     Mutex<HashMap<(String, String), WatchHandle>>,
    watches:     WatchStore,
    ledger:      Arc<Ledger>,
    tf:          Timeframe,
    dispatch_tx: mpsc::UnboundedSender<WatchSignalBatch>,
}

impl WatchEvaluator {
    pub fn new(
        watches:     WatchStore,
        ledger:      Arc<Ledger>,
        tf:          Timeframe,
        dispatch_tx: mpsc::UnboundedSender<WatchSignalBatch>,
    ) -> Self {
        Self {
            handles: Mutex::new(HashMap::new()),
            watches,
            ledger,
            tf,
            dispatch_tx,
        }
    }

    /// Drop cached strategy handles for a watch entry. Called by `delete_watch`
    /// so that stale handles are freed immediately rather than after grace eviction.
    pub fn remove_watch(&self, watch_id: &str) {
        let mut h = self.handles.lock();
        let before = h.len();
        h.retain(|(wid, _), _| wid != watch_id);
        let removed = before - h.len();
        if removed > 0 {
            debug!(watch_id, removed, "watch_evaluator: released strategy handles");
        }
    }
}

impl LedgerObserver for WatchEvaluator {
    fn on_advance(&self, symbol: &str, tf: Timeframe, outcome: &AdvanceOutcome) {
        if tf != self.tf {
            return;
        }

        let now_ms  = chrono::Utc::now().timestamp_millis();
        let is_stale = (now_ms - outcome.ts) > FRESHNESS_GATE_MS;

        // Step 1 — snapshot matching watch entries under a brief read lock.
        // `try_read` avoids blocking if an HTTP handler holds the write lock.
        let matched: Vec<MatchedWatch> = {
            let Ok(store) = self.watches.try_read() else {
                trace!(symbol, "watch_evaluator: WatchStore write-locked, skipping advance");
                return;
            };
            store.values()
                .filter(|slot| {
                    let e     = &slot.entry;
                    let w_tf  = e.timeframe.as_deref()
                        .and_then(parse_tf)
                        .unwrap_or(self.tf);
                    w_tf == tf
                        && e.symbols.iter().any(|s| s == symbol || s == "*")
                })
                .map(|slot| {
                    let e = &slot.entry;
                    MatchedWatch {
                        id:           e.id.clone(),
                        spec:         e.spec.clone(),
                        webhook_url:  e.webhook_url.clone(),
                        nats_subject: e.nats_subject.clone(),
                    }
                })
                .collect()
        }; // read lock released

        if matched.is_empty() {
            return;
        }

        // Step 2 — read bar window (single ledger read-lock, short).
        let window: Vec<Bar> = self.ledger
            .with_state(symbol, tf, |s| s.bar_window.iter().cloned().collect())
            .unwrap_or_default();
        let Some(bar) = window.last().cloned() else { return; };

        // Step 3 — evaluate under the handles mutex.
        let mut handles = self.handles.lock();

        for m in &matched {
            let key = (m.id.clone(), symbol.to_string());

            // Lazy-init: build strategy + acquire ledger handles on first bar.
            if !handles.contains_key(&key) {
                let (strategy_name, params) = m.spec.to_factory_args();
                match build_strategy_with_deps(&strategy_name, &params) {
                    Ok((strategy, deps)) => {
                        let dep_handles: Vec<IndicatorHandle> = deps.iter()
                            .filter_map(|dep| {
                                let spec = IndicatorSpec::from_config(
                                    dep.config.clone(), dep.source_tf,
                                ).ok()?;
                                match self.ledger.acquire_indicator(symbol, tf, spec) {
                                    Ok(h)  => Some(h),
                                    Err(e) => {
                                        warn!(watch_id=%m.id, err=%e, "watch: acquire indicator failed");
                                        None
                                    }
                                }
                            })
                            .collect();
                        debug!(
                            watch_id = %m.id,
                            symbol,
                            strategy = %strategy_name,
                            handles  = dep_handles.len(),
                            "watch_evaluator: strategy handle created",
                        );
                        handles.insert(key.clone(), WatchHandle {
                            strategy,
                            _indicator_handles: dep_handles,
                        });
                    }
                    Err(e) => {
                        warn!(watch_id=%m.id, err=%e, "watch_evaluator: build_strategy failed");
                        continue;
                    }
                }
            }

            let Some(wh) = handles.get_mut(&key) else { continue; };

            let mut sigs = wh.strategy.on_bar(&bar);
            sigs.extend(wh.strategy.on_window(&window));

            if sigs.is_empty() || is_stale {
                continue;
            }

            let batch = WatchSignalBatch {
                watch_id:     m.id.clone(),
                symbol:       symbol.to_string(),
                bar_ts:       outcome.ts,
                signals:      sigs.iter().map(WatchSignal::from).collect(),
                webhook_url:  m.webhook_url.clone(),
                nats_subject: m.nats_subject.clone(),
            };

            if let Err(e) = self.dispatch_tx.send(batch) {
                warn!(watch_id=%m.id, err=%e, "watch_evaluator: dispatch channel closed");
            }
        }
    }
}

// ── Dispatcher (async task) ───────────────────────────────────────────────────

/// Drains `WatchSignalBatch`es from the channel and dispatches each to its
/// configured targets: webhook POST and/or NATS subject.
///
/// Runs as a `tokio::spawn`ed task for the lifetime of the process.
pub async fn watch_dispatcher(
    nats: async_nats::Client,
    mut rx: mpsc::UnboundedReceiver<WatchSignalBatch>,
) {
    let http = reqwest::Client::builder()
        .timeout(std::time::Duration::from_secs(5))
        .build()
        .expect("reqwest client build");

    while let Some(batch) = rx.recv().await {
        let payload = match serde_json::to_vec(&batch) {
            Ok(v)  => v,
            Err(e) => {
                warn!(watch_id=%batch.watch_id, err=%e, "watch_dispatcher: serialize failed");
                continue;
            }
        };

        if let Some(ref url) = batch.webhook_url {
            match http.post(url)
                .header("Content-Type", "application/json")
                .body(payload.clone())
                .send()
                .await
            {
                Ok(r) if r.status().is_success() => {
                    debug!(watch_id=%batch.watch_id, url=%url, "watch_dispatcher: webhook ok");
                }
                Ok(r) => {
                    warn!(watch_id=%batch.watch_id, url=%url, status=%r.status(), "watch_dispatcher: webhook non-2xx");
                }
                Err(e) => {
                    warn!(watch_id=%batch.watch_id, url=%url, err=%e, "watch_dispatcher: webhook error");
                }
            }
        }

        if let Some(ref subject) = batch.nats_subject {
            if let Err(e) = nats.publish(subject.clone(), payload.into()).await {
                warn!(watch_id=%batch.watch_id, subject=%subject, err=%e, "watch_dispatcher: NATS publish failed");
            }
        }
    }

    tracing::info!("watch_dispatcher: channel closed, exiting");
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn parse_tf(s: &str) -> Option<Timeframe> {
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
