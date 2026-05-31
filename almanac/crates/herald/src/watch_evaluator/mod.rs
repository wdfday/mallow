//! Watch signal evaluator — `LedgerObserver` that runs strategy logic for
//! each `WatchEntry` and dispatches signals to webhook or NATS.
//!
//! Separated from the bot `Registry` so that watch signals never touch the
//! trade-execution path.
//!
//! ## Shared execution core
//!
//! Strategy handles are `registry::Handle` — the same V1/V2 execution unit
//! used by the bot registry. Watch entries get the same script-only
//! restriction (no named strategies) and identical warmup behaviour for free.
//! V2 (MTF) handles work when the ledger already maintains the declared HTF
//! bars via another hand or the ResampleManager; otherwise V2 degrades
//! gracefully (empty HTF window on warmup, fills in as bars arrive).
//!
//! ## Lifecycle
//!
//! - `Handle`s are created lazily on first matching bar per `(watch_id, symbol)`.
//! - `remove_watch` drops them explicitly when an entry is deleted via HTTP.
//! - Signals older than `FRESHNESS_GATE_MS` are suppressed (warm-up replay).

pub mod dispatcher;
pub mod types;

pub use dispatcher::watch_dispatcher;
pub use types::{WatchSignal, WatchSignalBatch};

use std::collections::HashMap;
use std::sync::Arc;

use alm_core::{Bar, Timeframe};
use alm_ledger::{AdvanceOutcome, Ledger, LedgerObserver};
use parking_lot::Mutex;
use tokio::sync::mpsc;
use tracing::{debug, trace, warn};

use crate::http::watch::WatchStore;
use crate::registry::{Handle, FRESHNESS_GATE_MS};

// ── WatchEvaluator ────────────────────────────────────────────────────────────

pub struct WatchEvaluator {
    /// Cached per-(watch_id, symbol) execution handle.
    handles:     Mutex<HashMap<(String, String), Handle>>,
    watches:     WatchStore,
    ledger:      Arc<Ledger>,
    /// Base timeframe of the ledger feed (typically M1).
    base_tf:     Timeframe,
    dispatch_tx: mpsc::UnboundedSender<WatchSignalBatch>,
}

impl WatchEvaluator {
    pub fn new(
        watches:     WatchStore,
        ledger:      Arc<Ledger>,
        base_tf:     Timeframe,
        dispatch_tx: mpsc::UnboundedSender<WatchSignalBatch>,
    ) -> Self {
        Self {
            handles: Mutex::new(HashMap::new()),
            watches,
            ledger,
            base_tf,
            dispatch_tx,
        }
    }

    /// Drop cached handles for a watch entry. Called by `delete_watch` so that
    /// stale handles are freed immediately rather than after grace eviction.
    pub fn remove_watch(&self, watch_id: &str) {
        let mut h = self.handles.lock();
        let before = h.len();
        h.retain(|(wid, _), _| wid != watch_id);
        let removed = before - h.len();
        if removed > 0 {
            debug!(watch_id, removed, "watch_evaluator: released handles");
        }
    }
}

impl LedgerObserver for WatchEvaluator {
    fn on_advance(&self, symbol: &str, tf: Timeframe, outcome: &AdvanceOutcome) {
        let now_ms = chrono::Utc::now().timestamp_millis();
        // Freshness measured from bar CLOSE, not open. `outcome.ts` is the bar's
        // OPEN — for an HTF bar that just closed it is up to one TF-duration old, so
        // gating on open would suppress every legitimate HTF watch signal. Also drop
        // bars whose OPEN is in the future (clock skew / replay). Mirrors
        // Registry::evaluate_and_publish.
        const FUTURE_OPEN_TOLERANCE_MS: i64 = 5_000;
        let close_ts    = outcome.ts + tf.duration_ms();
        let from_future = outcome.ts > now_ms + FUTURE_OPEN_TOLERANCE_MS;
        let is_stale    = (now_ms - close_ts) > FRESHNESS_GATE_MS || from_future;

        // Step 1 — snapshot matching watch entries under a brief read lock.
        // `try_read` avoids blocking if an HTTP handler holds the write lock.
        let matched: Vec<(String, String, Timeframe, Option<String>, Option<String>)> = {
            let Ok(store) = self.watches.try_read() else {
                trace!(symbol, "watch_evaluator: WatchStore write-locked, skipping");
                return;
            };
            store.values()
                .filter(|slot| {
                    let e    = &slot.entry;
                    let w_tf = e.timeframe.as_deref()
                        .and_then(|s| s.parse().ok())
                        .unwrap_or(self.base_tf);
                    w_tf == tf
                        && e.symbols.iter().any(|s| s == symbol || s == "*")
                })
                .map(|slot| {
                    let e    = &slot.entry;
                    let w_tf = e.timeframe.as_deref()
                        .and_then(|s| s.parse().ok())
                        .unwrap_or(self.base_tf);
                    (e.id.clone(), e.spec.script.clone(), w_tf,
                     e.webhook_url.clone(), e.nats_subject.clone())
                })
                .collect()
        }; // read lock released

        if matched.is_empty() {
            return;
        }

        // Step 2 — fetch the latest bar (single ledger read, short).
        let Some(bar): Option<Bar> = self.ledger
            .with_state(symbol, tf, |s| s.bar_window.back().cloned())
            .flatten()
        else { return; };

        // Step 3 — evaluate under the handles mutex.
        let mut handles = self.handles.lock();

        for (watch_id, script, target_tf, webhook_url, nats_subject) in &matched {
            let key = (watch_id.clone(), symbol.to_string());

            // Lazy-init: build a `Handle` (V1 or V2) on first bar.
            if !handles.contains_key(&key) {
                let hand_id = format!("watch.{}.{}", watch_id, symbol);
                match Handle::new(
                    hand_id,
                    String::new(),    // no helm
                    symbol.to_string(),
                    String::new(),    // no exchange context
                    false,
                    script.clone(),
                    &self.ledger,
                    self.base_tf,
                    *target_tf,
                ) {
                    Ok(handle) => {
                        debug!(
                            watch_id = %watch_id, %symbol,
                            target_tf = %target_tf,
                            "watch_evaluator: handle created"
                        );
                        handles.insert(key.clone(), handle);
                    }
                    Err(e) => {
                        warn!(watch_id = %watch_id, err = %e,
                              "watch_evaluator: Handle::new failed");
                        continue;
                    }
                }
            }

            let Some(handle) = handles.get_mut(&key) else { continue; };

            let sigs = handle.on_bar(&bar);

            if sigs.is_empty() || is_stale {
                continue;
            }

            let batch = WatchSignalBatch {
                watch_id:     watch_id.clone(),
                symbol:       symbol.to_string(),
                bar_ts:       outcome.ts,
                signals:      sigs.iter().map(WatchSignal::from).collect(),
                webhook_url:  webhook_url.clone(),
                nats_subject: nats_subject.clone(),
            };

            if let Err(e) = self.dispatch_tx.send(batch) {
                warn!(watch_id = %watch_id, err = %e,
                      "watch_evaluator: dispatch channel closed");
            }
        }
    }
}

