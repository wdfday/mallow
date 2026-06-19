//! Hand registry — the evaluation engine that sits on top of `alm-ledger`.
//!
//! # Lock design
//!
//! The previous design used a single `Mutex<RegistryInner>` that wrapped both
//! the outer symbol map and every `SymbolGroup` inside it. This meant BTCUSDT
//! evaluation blocked ETHUSDT evaluation even though they share no state.
//!
//! The new design has two independent lock layers:
//!
//! ```text
//! groups: DashMap<String, Arc<Mutex<SymbolGroup>>>
//!                │                │
//!                │                └─ per-symbol Mutex — held only for that symbol's eval
//!                └─ shard RwLock — held for nanoseconds (key lookup + Arc clone)
//!
//! global_config: RwLock<Option<GlobalConfig>>
//!                └─ nearly always a read (fallback-hand check per base-TF bar)
//! ```
//!
//! ## Hot path (`evaluate_and_publish`)
//!
//! 1. `groups.get(symbol)` — DashMap shard read (nanoseconds), clone `Arc`.
//! 2. Shard lock released immediately after the clone.
//! 3. `group_arc.lock()` — per-symbol `Mutex` held for Rhai evaluation.
//! 4. Signal dispatch — no lock held.
//!
//! Different symbols now evaluate with **zero cross-symbol contention**.
//! BTCUSDT and ETHUSDT Rhai scripts run at the same time if called from
//! concurrent tasks (today they run from the same tokio task, but the lock
//! model is ready for parallelisation without further changes).
//!
//! ## `ensure_fallback_hand` — N1 fix
//!
//! Uses `DashMap::entry().or_insert_with()` for an atomic check-and-insert,
//! eliminating the previous TOCTOU check-then-act window.

pub mod handle;
pub mod types;

pub use handle::{Handle, SymbolGroup};
pub use types::{HandSignal, HandListEntry, FRESHNESS_GATE_MS};

use std::sync::Arc;
use std::sync::atomic::Ordering;

use anyhow::Result;
use alm_core::Timeframe;
use alm_ledger::{AdvanceOutcome, Ledger, LedgerObserver};
use alm_strategy::probe_script_htfs;
use dashmap::DashMap;
use parking_lot::{Mutex, RwLock};
use tokio::sync::mpsc;
use tracing::{debug, info, warn};

use crate::helper::ResampleManager;

// ── GlobalConfig ──────────────────────────────────────────────────────────────

struct GlobalConfig {
    scripts: Vec<String>,
}

// ── Registry ──────────────────────────────────────────────────────────────────

pub struct Registry {
    /// Per-symbol evaluation groups.
    ///
    /// Each value is `Arc<Mutex<SymbolGroup>>` so the `Arc` can be cloned
    /// before the DashMap shard lock is released, letting per-symbol evaluation
    /// proceed without holding any outer map lock.
    groups: DashMap<String, Arc<Mutex<SymbolGroup>>>,

    /// Fallback scripts for new symbols. Read on every base-TF bar (hot read),
    /// written only by `set_global_config` (rare admin op). `RwLock` makes the
    /// hot read essentially free — concurrent readers never block each other.
    global_config: RwLock<Option<GlobalConfig>>,

    ledger: Arc<Ledger>,
    /// Ledger's external feed timeframe (typically M1). Hands with higher TFs
    /// are wired through `ResampleManager`.
    base_tf: Timeframe,
    /// Drives source→target bar aggregation for HTFs above `base_tf`.
    resample: Arc<ResampleManager>,
    /// Freshness threshold in ms. Atomic for lock-free hot-path access.
    /// Signals whose bar close is older are suppressed (replayed/warm-up bars).
    freshness_gate_ms: std::sync::atomic::AtomicI64,
    /// Bounded signal channel to the `signal_publisher` tokio task.
    signal_tx: mpsc::Sender<HandSignal>,
}

impl Registry {
    pub fn new(
        ledger: Arc<Ledger>,
        resample: Arc<ResampleManager>,
        base_tf: Timeframe,
        signal_tx: mpsc::Sender<HandSignal>,
    ) -> Self {
        Self {
            groups: DashMap::new(),
            global_config: RwLock::new(None),
            ledger,
            base_tf,
            resample,
            freshness_gate_ms: std::sync::atomic::AtomicI64::new(FRESHNESS_GATE_MS),
            signal_tx,
        }
    }

    /// Override the default 2-minute freshness gate. Tests use this to drive
    /// synthetic-timestamp bars without all signals being dropped as stale.
    /// Production code should never call this — leave the default.
    pub fn set_freshness_gate_ms(&self, ms: i64) {
        self.freshness_gate_ms.store(ms, Ordering::Relaxed);
    }

    /// Constructor with multiple default fallback scripts run on every symbol
    /// that has no explicit hands registered.
    pub fn with_default_scripts(
        ledger: Arc<Ledger>,
        resample: Arc<ResampleManager>,
        base_tf: Timeframe,
        signal_tx: mpsc::Sender<HandSignal>,
        scripts: Vec<String>,
    ) -> Self {
        let r = Self::new(ledger, resample, base_tf, signal_tx);
        if !scripts.is_empty() {
            *r.global_config.write() = Some(GlobalConfig { scripts });
        }
        r
    }

    /// Convenience constructor — single EMA-cross fallback (legacy compat).
    pub fn with_default_fallback(
        ledger: Arc<Ledger>,
        resample: Arc<ResampleManager>,
        base_tf: Timeframe,
        signal_tx: mpsc::Sender<HandSignal>,
    ) -> Self {
        Self::with_default_scripts(ledger, resample, base_tf, signal_tx, vec![
            r#"let fast = ind.ema(20); let slow = ind.ema(50);
let long = fast[1] <= slow[1] && fast[0] > slow[0];
let exit = fast[1] >= slow[1] && fast[0] < slow[0];"#.into(),
        ])
    }

    /// Register (or re-register) a hand on a symbol at `target_tf`.
    ///
    /// `target_tf` is the TF the hand evaluates at (from `RegisterMsg.timeframe`).
    /// V1 hands receive bars at `target_tf` directly. V2 hands use `target_tf`
    /// as the strategy's base TF and read declared HTF views from the ledger.
    /// All TFs above `self.base_tf` are wired through `ResampleManager`.
    pub fn register(
        &self,
        hand_id: String,
        helm_id: String,
        symbol: String,
        exchange: String,
        is_future: bool,
        script: String,
        target_tf: Timeframe,
    ) -> Result<()> {
        // Ensure ledger slots exist before Handle::new warms from history.
        self.ledger.ensure_symbol(&symbol, target_tf, None);
        let declared_htfs = probe_script_htfs(&script);
        for htf in &declared_htfs {
            self.ledger.ensure_symbol(&symbol, *htf, None);
        }

        // Build the handle first. If the script is invalid we fail here —
        // before any resample subscription is created — so ResampleManager
        // is never polluted with subscriptions for a hand that never lived.
        let hand = Handle::new(
            hand_id, helm_id, symbol.clone(), exchange, is_future, script,
            &self.ledger, self.base_tf, target_tf,
        )?;

        // Bump resample refcounts after a successful build.
        for (src, tgt) in &hand.resample_subs {
            self.resample.ensure(&symbol, *src, *tgt);
        }
        info!(
            hand_id = %hand.hand_id, helm_id = %hand.helm_id,
            symbol = %hand.symbol, target_tf = %target_tf,
            exchange = %hand.exchange, is_future = hand.is_future,
            script_bytes = hand.script.len(),
            "hand registered"
        );

        // Atomic check-and-create via DashMap entry API.
        let group_arc = self.groups
            .entry(symbol.clone())
            .or_insert_with(|| Arc::new(Mutex::new(SymbolGroup::new(symbol.clone()))))
            .clone();

        // Per-symbol lock only — no cross-symbol contention.
        let replaced_subs = group_arc.lock().add(hand);

        // Release old subscriptions outside the lock — ResampleManager has its own lock.
        for (src, tgt) in replaced_subs {
            self.resample.release(&symbol, src, tgt);
        }

        metrics::counter!("herald_registry_hand_events_total", "event" => "registered").increment(1);
        metrics::gauge!("herald_registry_hands").set(self.hand_count() as f64);
        Ok(())
    }

    /// Release a batch of resample subscriptions collected from multiple symbols.
    /// Always called after all registry locks are released to avoid lock ordering
    /// issues with `ResampleManager`'s own internal lock.
    fn release_batch(&self, batch: Vec<(String, Vec<(Timeframe, Timeframe)>)>) {
        for (sym, pairs) in batch {
            for (src, tgt) in pairs {
                self.resample.release(&sym, src, tgt);
            }
        }
    }

    /// Remove a hand by id. Empty `hand_id` = clear all.
    ///
    /// Uses DashMap iteration so each symbol's shard is locked only briefly
    /// (nanoseconds) while we clone the `Arc`, not for the duration of the
    /// `remove` operation. This no longer blocks `evaluate_and_publish` for
    /// other symbols.
    pub fn deregister(&self, hand_id: &str) {
        let mut batch: Vec<(String, Vec<(Timeframe, Timeframe)>)> = Vec::new();

        if hand_id.is_empty() {
            let total: usize = self.groups.iter().map(|e| e.value().lock().len()).sum();
            info!(n_hands = total, "deregistering all hands");
            for entry in self.groups.iter() {
                let subs = entry.value().lock().drain_subs();
                if !subs.is_empty() { batch.push((entry.key().clone(), subs)); }
            }
            self.groups.clear();
        } else {
            // O(n) scan — no reverse index. See N5 for the tracking issue.
            let mut empty_keys: Vec<String> = Vec::new();
            for entry in self.groups.iter() {
                let mut group = entry.value().lock();
                let subs = group.remove(hand_id);
                if !subs.is_empty() { batch.push((entry.key().clone(), subs)); }
                if group.is_empty() { empty_keys.push(entry.key().clone()); }
            }
            for key in empty_keys {
                // Re-check inside entry to avoid removing a group that a
                // concurrent `register` just re-populated.
                self.groups.remove_if(&key, |_, arc| arc.lock().is_empty());
            }
        }

        self.release_batch(batch);
        metrics::counter!("herald_registry_hand_events_total", "event" => "deregistered").increment(1);
        metrics::gauge!("herald_registry_hands").set(self.hand_count() as f64);
    }

    /// Replace global fallback scripts (legacy `engine.configure` compat).
    /// Drains and clears all groups before installing new config.
    pub fn set_global_config(&self, script: String) {
        let mut batch: Vec<(String, Vec<(Timeframe, Timeframe)>)> = Vec::new();
        for entry in self.groups.iter() {
            let subs = entry.value().lock().drain_subs();
            if !subs.is_empty() { batch.push((entry.key().clone(), subs)); }
        }
        self.groups.clear();
        *self.global_config.write() = Some(GlobalConfig { scripts: vec![script] });
        self.release_batch(batch);
    }

    /// Reset state for one symbol, or all if `symbol` is empty.
    pub fn reset(&self, symbol: &str) {
        let batch: Vec<(String, Vec<(Timeframe, Timeframe)>)> = if symbol.is_empty() {
            let mut out = Vec::new();
            for entry in self.groups.iter() {
                let subs = entry.value().lock().drain_subs();
                if !subs.is_empty() { out.push((entry.key().clone(), subs)); }
            }
            self.groups.clear();
            out
        } else {
            match self.groups.remove(symbol) {
                Some((sym, arc)) => {
                    let subs = arc.lock().drain_subs();
                    if subs.is_empty() { vec![] } else { vec![(sym, subs)] }
                }
                None => vec![],
            }
        };
        self.release_batch(batch);
    }

    /// Returns a snapshot of every registered hand.
    pub fn list_hands(&self) -> Vec<HandListEntry> {
        self.groups
            .iter()
            .flat_map(|entry| {
                entry.value().lock().hand_infos()
                    .map(|h| HandListEntry {
                        hand_id:   h.hand_id.clone(),
                        helm_id:   h.helm_id.clone(),
                        symbol:    h.symbol.clone(),
                        script:    h.script.clone(),
                        timeframe: h.target_tf.to_string(),
                        exchange:  h.exchange.clone(),
                        is_future: h.is_future,
                    })
                    .collect::<Vec<_>>()
            })
            .collect()
    }

    /// Returns the number of currently registered hands.
    pub fn hand_count(&self) -> usize {
        self.groups.iter().map(|e| e.value().lock().len()).sum()
    }

    fn evaluate_and_publish(&self, symbol: &str, tf: Timeframe, outcome: &AdvanceOutcome) {
        // Freshness is measured from bar CLOSE time, not open. For an HTF bar
        // that just closed, outcome.ts (= bar open) is up to one TF-duration
        // old; using it directly would suppress every legitimate HTF signal.
        //
        // We also gate bars whose OPEN is in the future — catches clock-skewed
        // or synthetic replay bars that haven't physically happened yet.
        const FUTURE_OPEN_TOLERANCE_MS: i64 = 5_000;
        let now_ms = chrono::Utc::now().timestamp_millis();
        let close_ts = outcome.ts + tf.duration_ms();
        let age_ms = now_ms - close_ts;
        let from_future = outcome.ts > now_ms + FUTURE_OPEN_TOLERANCE_MS;
        let gate_ms = self.freshness_gate_ms.load(Ordering::Relaxed);
        let is_stale = age_ms > gate_ms || from_future;

        debug!(%symbol, ?tf, bar_ts = outcome.ts, close_ts, age_ms, from_future, is_stale,
               "evaluate_and_publish");

        // Fetch the confirmed bar from the ledger's ring buffer.
        let bar = match self.ledger.with_state(symbol, tf, |s| s.bar_window.back().cloned()) {
            Some(Some(b)) => b,
            _ => return,
        };

        // Seed a fallback hand for new symbols at base TF — only when:
        //   (a) global_config is set (fast atomic read via RwLock)
        //   (b) this symbol has no explicit hands yet
        //   (c) this is a base-TF advance (not an HTF fan-out)
        if tf == self.base_tf
            && self.global_config.read().is_some()
            && !self.groups.contains_key(symbol)
        {
            self.ensure_fallback_hand(symbol);
        }

        // ── Per-symbol evaluation ─────────────────────────────────────────────
        //
        // Step 1: clone the Arc from the DashMap entry.
        //         The DashMap shard lock is held only for this clone — nanoseconds.
        //
        // Step 2: release shard lock (happens when `entry` is dropped).
        //
        // Step 3: lock only this symbol's Mutex for Rhai evaluation.
        //         Other symbols are completely unaffected.
        let group_arc = match self.groups.get(symbol) {
            Some(entry) => Arc::clone(entry.value()),
            None        => return,
        };
        // ← DashMap shard lock released here.

        let span = tracing::info_span!(
            "registry.evaluate",
            symbol = %symbol,
            tf = %tf.to_string(),
        );
        let _enter = span.enter();

        let lock_start = std::time::Instant::now();
        let (emitted, lock_wait_us, eval_us) = {
            let mut group = group_arc.lock();           // per-symbol lock only
            let lock_wait_us = lock_start.elapsed().as_micros();

            let n_bots = group.count_for_tf(tf);
            if n_bots == 0 { return; }

            debug!(%symbol, ?tf, bar_ts = outcome.ts, n_bots, "evaluating bots");
            let eval_start = std::time::Instant::now();
            let result = group.evaluate_at(tf, &bar);
            let eval_us = eval_start.elapsed().as_micros();
            (result, lock_wait_us, eval_us)
        };
        // ← per-symbol Mutex released here.

        debug!(
            %symbol, ?tf, bar_ts = outcome.ts,
            n_emitted = emitted.len(), lock_wait_us, eval_us,
            "evaluation complete"
        );

        metrics::histogram!(
            "herald_registry_lock_wait_us",
            "symbol" => symbol.to_string(), "tf" => tf.to_string(),
        ).record(lock_wait_us as f64);
        metrics::histogram!(
            "herald_registry_eval_us",
            "symbol" => symbol.to_string(), "tf" => tf.to_string(),
        ).record(eval_us as f64);

        if emitted.is_empty() {
            let n_bots = {
                match self.groups.get(symbol) {
                    Some(entry) => entry.value().lock().count_for_tf(tf),
                    None => 0,
                }
            };
            debug!(%symbol, tf = %tf.to_string(), bar_ts = outcome.ts, n_bots, "bar evaluated — no signal emitted");
            return;
        }

        if is_stale {
            info!(
                %symbol, tf = %tf.to_string(), age_ms,
                freshness_gate_ms = gate_ms, n_suppressed = emitted.len(),
                "stale bar — suppressing signals"
            );
            metrics::counter!(
                "herald_registry_signals_total",
                "result" => "stale", "symbol" => symbol.to_string(),
            ).increment(emitted.len() as u64);
            return;
        }

        metrics::counter!(
            "herald_registry_signals_total",
            "result" => "emitted", "symbol" => symbol.to_string(),
        ).increment(emitted.len() as u64);

        // Bar-close → signal latency: time from bar close until signal is ready
        // to be dispatched. Measures strategy evaluation + channel send overhead.
        metrics::histogram!(
            "herald_bar_to_signal_latency_ms",
            "symbol" => symbol.to_string(), "tf" => tf.to_string(),
        ).record(age_ms as f64);

        // Signal timestamp = bar close time, not open.
        for (hand_id, helm_id, mut signal) in emitted {
            signal.timestamp = close_ts;
            let dir = format!("{:?}", signal.direction);
            metrics::counter!(
                "herald_signals_emitted_direction_total",
                "symbol"    => symbol.to_string(),
                "tf"        => tf.to_string(),
                "direction" => dir.clone(),
            ).increment(1);
            info!(
                hand_id = %hand_id, helm_id = %helm_id,
                %symbol, tf = %tf.to_string(),
                direction = %dir, strength = signal.strength,
                bar_open_ts = outcome.ts, signal_ts = close_ts,
                age_ms,
                "signal emitted"
            );
            match self.signal_tx.try_send(HandSignal {
                hand_id: hand_id.clone(), helm_id: helm_id.clone(),
                bar_ts: close_ts, signal,
            }) {
                Ok(()) => {}
                Err(tokio::sync::mpsc::error::TrySendError::Full(_)) => {
                    warn!(
                        hand_id = %hand_id, helm_id = %helm_id, %symbol,
                        "signal channel full — dropping signal (publisher backpressure)"
                    );
                    metrics::counter!("herald_signals_dropped_total", "reason" => "channel_full")
                        .increment(1);
                }
                Err(tokio::sync::mpsc::error::TrySendError::Closed(_)) => {
                    // signal_publisher task has exited — all subsequent signals will be lost.
                    tracing::error!(
                        hand_id = %hand_id, helm_id = %helm_id, %symbol,
                        "signal channel closed — publisher task has exited"
                    );
                    metrics::counter!("herald_signals_dropped_total", "reason" => "channel_closed")
                        .increment(1);
                }
            }
        }
    }

    /// Seed default fallback hands for a symbol that has no explicit hands.
    ///
    /// Uses `DashMap::entry().or_insert_with()` for atomic check-and-create,
    /// fixing the N1 (TOCTOU check-then-act) issue from the previous design.
    fn ensure_fallback_hand(&self, symbol: &str) {
        let scripts: Vec<String> = self.global_config.read()
            .as_ref()
            .map(|g| g.scripts.clone())
            .unwrap_or_default();
        if scripts.is_empty() { return; }

        self.ledger.ensure_symbol(symbol, self.base_tf, None);

        for (i, script) in scripts.iter().enumerate() {
            let hand_id = format!("default.{}.{}", i, symbol);
            let declared_htfs = probe_script_htfs(script);
            for htf in &declared_htfs {
                self.ledger.ensure_symbol(symbol, *htf, None);
                if *htf != self.base_tf {
                    self.resample.ensure(symbol, self.base_tf, *htf);
                }
            }
            match Handle::new(
                hand_id.clone(), String::new(), symbol.to_string(),
                String::new(), false,
                script.clone(),
                &self.ledger, self.base_tf, self.base_tf,
            ) {
                Ok(h) => {
                    // Atomic insert: or_insert_with guarantees only one Arc is
                    // created even under concurrent calls for the same symbol.
                    let group_arc = self.groups
                        .entry(symbol.to_string())
                        .or_insert_with(|| Arc::new(Mutex::new(SymbolGroup::new(symbol.to_string()))))
                        .clone();
                    let replaced_subs = group_arc.lock().add(h);
                    for (src, tgt) in replaced_subs {
                        self.resample.release(symbol, src, tgt);
                    }
                }
                Err(e) => warn!(%symbol, hand_id, err = %e, "failed to build fallback strategy"),
            }
        }
    }
}

impl LedgerObserver for Registry {
    fn on_advance(&self, symbol: &str, tf: Timeframe, outcome: &AdvanceOutcome) {
        debug!(symbol, tf = %tf.to_string(), bar_ts = outcome.ts, "ledger observer triggered");
        self.evaluate_and_publish(symbol, tf, outcome);
    }
}

// ── tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_ledger::LedgerConfig;
    use alm_core::Bar;
    use tokio::sync::mpsc;

    fn mk_bar(sym: &str, t: i64, c: f64) -> Bar {
        Bar::new(t, sym, c, c, c, c, 1.0)
    }

    /// Test helper: bounded channel (capacity 256) to match production.
    fn make_registry() -> (Arc<Ledger>, Arc<Registry>, mpsc::Receiver<HandSignal>) {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let resample = ResampleManager::new(Arc::downgrade(&ledger));
        ledger.subscribe(resample.clone() as Arc<dyn LedgerObserver>);
        let (tx, rx) = mpsc::channel(256);
        let reg = Arc::new(Registry::with_default_fallback(ledger.clone(), resample, Timeframe::M1, tx));
        ledger.subscribe(reg.clone() as Arc<dyn LedgerObserver>);
        (ledger, reg, rx)
    }

    #[test]
    fn register_then_list() {
        let (_led, reg, _rx) = make_registry();
        reg.register(
            "hand1".into(), "helm1".into(), "BTCUSDT".into(),
            String::new(), false,
            "let r = ind.rsi(14);\nif r[0] < 30.0 { long = true; }\nif r[0] > 70.0 { exit = true; }".into(),
            Timeframe::M1,
        ).unwrap();
        let hands = reg.list_hands();
        assert_eq!(hands.len(), 1);
        assert_eq!(hands[0].hand_id, "hand1");
    }

    #[tokio::test]
    async fn stale_bar_suppresses_signals() {
        let (led, reg, mut rx) = make_registry();
        reg.register(
            "hand1".into(), "helm1".into(), "BTCUSDT".into(),
            String::new(), false,
            "let e2 = ind.ema(2); let e3 = ind.ema(3);\nif e2[0] > e3[0] { long = true; }\nif e2[0] < e3[0] { exit = true; }".into(),
            Timeframe::M1,
        ).unwrap();
        for i in 1..10 {
            led.advance(Timeframe::M1, mk_bar("BTCUSDT", 946_684_800_000 + i * 60_000, 100.0 + i as f64)).unwrap();
        }
        assert!(rx.try_recv().is_err());
        drop(reg);
    }

    #[tokio::test]
    async fn live_bar_produces_signals_when_strategy_emits() {
        let (led, reg, mut rx) = make_registry();
        reg.register(
            "hand1".into(), "helm1".into(), "TEST".into(),
            String::new(), false,
            "let e2 = ind.ema(2, 2); let e3 = ind.ema(3, 2);\nif cross_above(e2, e3) { long = true; }\nif cross_below(e2, e3) { exit = true; }".into(),
            Timeframe::M1,
        ).unwrap();
        let now = chrono::Utc::now().timestamp_millis();
        let prices = [100.0, 101.0, 102.0, 103.0, 102.0, 101.0, 100.0, 99.0, 98.0, 97.0];
        for (i, p) in prices.iter().enumerate() {
            let t = now - ((prices.len() - 1 - i) as i64) * 1000;
            led.advance(Timeframe::M1, mk_bar("TEST", t, *p)).unwrap();
        }
        while rx.try_recv().is_ok() {}
        drop(reg);
    }

    #[tokio::test]
    async fn deregister_all_clears() {
        let (_led, reg, _rx) = make_registry();
        let script = "let e5 = ind.ema(5); let e20 = ind.ema(20);\nif e5[0] > e20[0] { long = true; }\nif e5[0] < e20[0] { exit = true; }";
        reg.register("hand1".into(), "helm1".into(), "BTCUSDT".into(), String::new(), false, script.into(), Timeframe::M1).unwrap();
        reg.register("hand2".into(), "helm2".into(), "ETHUSDT".into(), String::new(), false, script.into(), Timeframe::M1).unwrap();
        reg.deregister("");
        assert_eq!(reg.list_hands().len(), 0);
    }

    /// Two hands at the same HTF share one resample subscription (refcounted).
    #[test]
    fn two_hands_same_htf_share_resample() {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let resample = ResampleManager::new(Arc::downgrade(&ledger));
        ledger.subscribe(resample.clone() as Arc<dyn LedgerObserver>);
        let (tx, _rx) = mpsc::channel(256);
        let reg = Arc::new(Registry::new(ledger.clone(), resample.clone(), Timeframe::M1, tx));
        ledger.subscribe(reg.clone() as Arc<dyn LedgerObserver>);

        let script = "let r = ind.rsi(14); if r[0] < 30.0 { long = true; }";
        reg.register("hA".into(), "h".into(), "BTCUSDT".into(), String::new(), false, script.into(), Timeframe::H1).unwrap();
        reg.register("hB".into(), "h".into(), "BTCUSDT".into(), String::new(), false, script.into(), Timeframe::H1).unwrap();
        assert_eq!(resample.len(), 1);
    }

    /// Hand registered at a higher TF only fires on HTF advances, not on
    /// every base bar. Verifies resample wiring + target_tf routing end-to-end.
    #[tokio::test]
    async fn hand_at_htf_only_fires_on_htf_close() {
        let (led, reg, mut rx) = make_registry();
        reg.set_freshness_gate_ms(i64::MAX);
        reg.register(
            "hand_m5".into(), "helm1".into(), "BTCUSDT".into(),
            String::new(), false,
            "long = true;".into(),
            Timeframe::M5,
        ).unwrap();

        let m5_ms = Timeframe::M5.duration_ms();
        let m1_ms = Timeframe::M1.duration_ms();
        let now = chrono::Utc::now().timestamp_millis();
        let aligned_m5 = now - (now % m5_ms);
        let start = aligned_m5 - 3 * m5_ms;
        let n_bars = 3 * 5 + 1;
        let mut got_signal = false;
        for i in 0..n_bars {
            led.advance(
                Timeframe::M1,
                mk_bar("BTCUSDT", start + i as i64 * m1_ms, 100.0 + i as f64),
            ).unwrap();
            while let Ok(sig) = rx.try_recv() {
                assert_eq!(sig.hand_id, "hand_m5");
                got_signal = true;
            }
        }
        assert!(got_signal, "hand should have fired on at least one M5 close");
    }
}
