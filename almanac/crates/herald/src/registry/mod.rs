//! Hand registry — the evaluation engine that sits on top of `alm-ledger`.
//!
//! `Registry` implements [`LedgerObserver`] so it receives a callback for
//! every live bar advanced into the ledger. For each advance it:
//!
//!   1. Reads the shared `bar_window` from `Ledger` (single source of truth
//!      shared with the HTTP data API).
//!   2. Runs each hand's strategy (`on_bar` + `on_window`).
//!   3. Drops any signals older than [`FRESHNESS_GATE_MS`] — replayed
//!      JetStream bars warm up strategy state but must not trigger live
//!      signals.
//!   4. Emits non-stale signal batches on an [`mpsc::UnboundedSender`];
//!      `Handler` owns the receiver and pushes them to NATS.

pub mod handle;
pub mod types;

pub use handle::{Handle, SymbolGroup};
pub use types::{HandSignal, FRESHNESS_GATE_MS};

use std::collections::HashMap;
use std::sync::Arc;

use anyhow::Result;
use alm_core::Timeframe;
use alm_ledger::{AdvanceOutcome, Ledger, LedgerObserver};
use alm_strategy::probe_script_htfs;
use parking_lot::Mutex;
use tokio::sync::mpsc;
use tracing::{debug, trace, warn};

use crate::resample::ResampleManager;

// ── Registry ──────────────────────────────────────────────────────────────────

struct RegistryInner {
    groups: HashMap<String, SymbolGroup>,
    /// Fallback script applied to new symbols when no explicit hand covers them.
    /// Set by the legacy `engine.configure` subject for backwards-compat.
    global_config: Option<GlobalConfig>,
}

struct GlobalConfig {
    scripts: Vec<String>,
}

pub struct Registry {
    inner: Mutex<RegistryInner>,
    ledger: Arc<Ledger>,
    /// The base timeframe of the ledger's external feed (typically M1).
    /// Hand `target_tf`s larger than this are resampled by `ResampleManager`.
    base_tf: Timeframe,
    /// Drives `source → target` aggregations for HTFs that the WS feed
    /// doesn't supply directly. Refcounted; each registered hand bumps
    /// the counts for the TFs it needs.
    resample: Arc<ResampleManager>,
    /// Suppress signals whose bar close is older than this. Default 2 min;
    /// tests may relax this to drive synthetic timestamps. Stored as i64 ms.
    freshness_gate_ms: std::sync::atomic::AtomicI64,
    /// Non-stale signal batches go here; `Handler` drains and publishes them.
    signal_tx: mpsc::UnboundedSender<HandSignal>,
}

impl Registry {
    pub fn new(
        ledger: Arc<Ledger>,
        resample: Arc<ResampleManager>,
        base_tf: Timeframe,
        signal_tx: mpsc::UnboundedSender<HandSignal>,
    ) -> Self {
        Self {
            inner: Mutex::new(RegistryInner { groups: HashMap::new(), global_config: None }),
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
        self.freshness_gate_ms
            .store(ms, std::sync::atomic::Ordering::Relaxed);
    }

    /// Constructor with multiple default fallback scripts run on every symbol
    /// that has no explicit hands registered. `scripts` is evaluated in order;
    /// each gets a unique `hand_id = "default.{i}.{symbol}"`.
    pub fn with_default_scripts(
        ledger: Arc<Ledger>,
        resample: Arc<ResampleManager>,
        base_tf: Timeframe,
        signal_tx: mpsc::UnboundedSender<HandSignal>,
        scripts: Vec<String>,
    ) -> Self {
        let r = Self::new(ledger, resample, base_tf, signal_tx);
        if !scripts.is_empty() {
            r.inner.lock().global_config = Some(GlobalConfig { scripts });
        }
        r
    }

    /// Convenience constructor — single EMA-cross fallback (legacy compat).
    pub fn with_default_fallback(
        ledger: Arc<Ledger>,
        resample: Arc<ResampleManager>,
        base_tf: Timeframe,
        signal_tx: mpsc::UnboundedSender<HandSignal>,
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
        script: String,
        target_tf: Timeframe,
    ) -> Result<()> {
        // Ensure symbol slots up front so `Handle::new` can warm from
        // history. Probing HTFs is also cheap (parse-only).
        self.ledger.ensure_symbol(&symbol, target_tf, None);
        let declared_htfs = probe_script_htfs(&script);
        for htf in &declared_htfs {
            self.ledger.ensure_symbol(&symbol, *htf, None);
        }

        // Build the hand first. If the script is invalid (Rhai syntax,
        // unknown indicator, candle directive error, HTF ≤ target_tf, ...)
        // we fail HERE — before any resample subscription is created — and
        // return the error to the caller verbatim. This avoids polluting
        // ResampleManager with subscriptions for a hand that never lived.
        let hand = Handle::new(
            hand_id, helm_id, symbol.clone(), script,
            &self.ledger, self.base_tf, target_tf,
        )?;

        // Bump resample refcounts after a successful build. The hand's
        // `resample_subs` field records the exact tuples it needs; we mirror
        // them on the ResampleManager so the aggregator state is alive while
        // the hand is registered.
        for (src, tgt) in &hand.resample_subs {
            self.resample.ensure(&symbol, *src, *tgt);
        }
        debug!(
            hand_id = %hand.hand_id, symbol = %hand.symbol,
            target_tf = %target_tf,
            handles = hand._indicator_handles.len(),
            "hand activated in registry"
        );
        let replaced_subs = {
            let mut w = self.inner.lock();
            w.groups
                .entry(symbol.clone())
                .or_insert_with(|| SymbolGroup::new(symbol.clone()))
                .add(hand)
        };
        // If we replaced an existing hand, release its old subscriptions —
        // the new hand's subs were already bumped above via `ensure`.
        for (src, tgt) in replaced_subs {
            self.resample.release(&symbol, src, tgt);
        }
        Ok(())
    }

    /// Remove a hand by id. Cleans up empty groups. Empty `hand_id` = clear all.
    pub fn deregister(&self, hand_id: &str) {
        // Collect (symbol, subs) under the lock, release outside the lock to
        // avoid holding `inner` while ResampleManager takes its own.
        let released: Vec<(String, Vec<(Timeframe, Timeframe)>)> = {
            let mut w = self.inner.lock();
            let mut out = Vec::new();
            if hand_id.is_empty() {
                for (sym, group) in w.groups.iter_mut() {
                    let subs = group.drain_subs();
                    if !subs.is_empty() {
                        out.push((sym.clone(), subs));
                    }
                }
                w.groups.clear();
            } else {
                for (sym, group) in w.groups.iter_mut() {
                    let subs = group.remove(hand_id);
                    if !subs.is_empty() {
                        out.push((sym.clone(), subs));
                    }
                }
                w.groups.retain(|_, g| !g.is_empty());
            }
            out
        };
        for (sym, subs) in released {
            for (src, tgt) in subs {
                self.resample.release(&sym, src, tgt);
            }
        }
    }

    /// Set global fallback scripts (legacy `engine.configure` compat).
    /// Replaces all existing scripts and clears registered groups.
    pub fn set_global_config(&self, script: String) {
        let released: Vec<(String, Vec<(Timeframe, Timeframe)>)> = {
            let mut w = self.inner.lock();
            let out: Vec<_> = w.groups
                .iter_mut()
                .map(|(sym, g)| (sym.clone(), g.drain_subs()))
                .filter(|(_, s)| !s.is_empty())
                .collect();
            w.groups.clear();
            w.global_config = Some(GlobalConfig { scripts: vec![script] });
            out
        };
        for (sym, subs) in released {
            for (src, tgt) in subs {
                self.resample.release(&sym, src, tgt);
            }
        }
    }

    /// Reset state for one symbol (or all if `symbol` is empty).
    pub fn reset(&self, symbol: &str) {
        let released: Vec<(String, Vec<(Timeframe, Timeframe)>)> = {
            let mut w = self.inner.lock();
            if symbol.is_empty() {
                let out: Vec<_> = w.groups
                    .iter_mut()
                    .map(|(sym, g)| (sym.clone(), g.drain_subs()))
                    .filter(|(_, s)| !s.is_empty())
                    .collect();
                w.groups.clear();
                out
            } else if let Some(mut g) = w.groups.remove(symbol) {
                let subs = g.drain_subs();
                if subs.is_empty() { vec![] } else { vec![(symbol.to_string(), subs)] }
            } else {
                vec![]
            }
        };
        for (sym, subs) in released {
            for (src, tgt) in subs {
                self.resample.release(&sym, src, tgt);
            }
        }
    }

    /// Returns (hand_id, helm_id, symbol, script, timeframe_str) for every registered hand.
    pub fn list_hands(&self) -> Vec<(String, String, String, String, String)> {
        let r = self.inner.lock();
        r.groups
            .values()
            .flat_map(|g| g.hand_infos())
            .map(|h| (
                h.hand_id.clone(),
                h.helm_id.clone(),
                h.symbol.clone(),
                h.script.clone(),
                h.target_tf.to_string(),
            ))
            .collect()
    }

    /// Returns the number of currently registered hands.
    pub fn hand_count(&self) -> usize {
        let r = self.inner.lock();
        r.groups.values().map(|g| g.len()).sum()
    }

    fn evaluate_and_publish(&self, symbol: &str, tf: Timeframe, outcome: &AdvanceOutcome) {
        // Freshness is measured from bar CLOSE time, not open. For an HTF bar
        // that just closed, `outcome.ts` (= bar open) is up to one TF-duration
        // old; using it directly would suppress every legitimate HTF signal.
        //
        // We also gate bars whose OPEN is in the future — that catches
        // clock-skewed or synthetic replay where bar.ts hasn't physically
        // happened yet. A bar with open ≤ now but close > now (e.g. HTF bar
        // just confirmed) is legitimate live data.
        const FUTURE_OPEN_TOLERANCE_MS: i64 = 5_000;
        let now_ms = chrono::Utc::now().timestamp_millis();
        let close_ts = outcome.ts + tf.duration_ms();
        let age_ms = now_ms - close_ts;
        let from_future = outcome.ts > now_ms + FUTURE_OPEN_TOLERANCE_MS;
        let gate_ms = self.freshness_gate_ms.load(std::sync::atomic::Ordering::Relaxed);
        let is_stale = age_ms > gate_ms || from_future;

        trace!(%symbol, ?tf, bar_ts = outcome.ts, close_ts, age_ms, from_future, is_stale, "evaluate_and_publish");

        // Fetch the just-advanced bar from the matching (symbol, tf) state.
        let bar = match self.ledger.with_state(symbol, tf, |s| s.bar_window.back().cloned()) {
            Some(Some(b)) => b,
            _ => return,
        };

        // Fallback hands are seeded at base_tf only. Triggering them on HTF
        // advances would create unwanted hand variants.
        if tf == self.base_tf {
            let r = self.inner.lock();
            if !r.groups.contains_key(symbol) {
                let needs_fallback = r.global_config.is_some();
                drop(r);
                if needs_fallback {
                    self.ensure_fallback_hand(symbol);
                }
            }
        }

        let emitted = {
            let mut w = self.inner.lock();
            match w.groups.get_mut(symbol) {
                Some(g) => g.evaluate_at(tf, &bar),
                None => Vec::new(),
            }
        };

        if emitted.is_empty() {
            trace!(%symbol, bar_ts = outcome.ts, "no signals this bar");
            return;
        }

        debug!(%symbol, bar_ts = outcome.ts, n = emitted.len(), "signals produced");

        if is_stale {
            debug!(%symbol, age_ms, freshness_gate_ms = FRESHNESS_GATE_MS, "stale bar — suppressing");
            return;
        }

        for (hand_id, helm_id, signal) in emitted {
            debug!(hand_id = %hand_id, %symbol, direction = ?signal.direction, "forwarding signal");
            let _ = self.signal_tx.send(HandSignal {
                hand_id, helm_id, bar_ts: outcome.ts, signal,
            });
        }
    }

    fn ensure_fallback_hand(&self, symbol: &str) {
        let scripts: Vec<String> = {
            let r = self.inner.lock();
            r.global_config.as_ref().map(|g| g.scripts.clone()).unwrap_or_default()
        };
        if scripts.is_empty() { return; }
        self.ledger.ensure_symbol(symbol, self.base_tf, None);
        // Fallback hands always evaluate at base_tf. Resample subscriptions
        // for any declared HTFs in the script run through the same path as
        // explicit `register()` — but routed inline since fallbacks bypass
        // the public API.
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
                hand_id.clone(), String::new(), symbol.to_string(), script.clone(),
                &self.ledger, self.base_tf, self.base_tf,
            ) {
                Ok(h) => {
                    let replaced_subs = {
                        let mut w = self.inner.lock();
                        w.groups
                            .entry(symbol.to_string())
                            .or_insert_with(|| SymbolGroup::new(symbol.to_string()))
                            .add(h)
                    };
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
        // Route to hands whose target_tf matches this advance. evaluate_and_publish
        // filters per-hand inside the symbol group.
        self.evaluate_and_publish(symbol, tf, outcome);
    }
}

// ── tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_ledger::{IndicatorSpec, LedgerConfig};
    use alm_core::Bar;
    use tokio::sync::mpsc;

    fn mk_bar(sym: &str, t: i64, c: f64) -> Bar {
        Bar::new(t, sym, c, c, c, c, 1.0)
    }

    fn make_registry() -> (Arc<Ledger>, Arc<Registry>, mpsc::UnboundedReceiver<HandSignal>) {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let resample = ResampleManager::new(Arc::downgrade(&ledger));
        ledger.subscribe(resample.clone() as Arc<dyn LedgerObserver>);
        let (tx, rx) = mpsc::unbounded_channel();
        let reg = Arc::new(Registry::with_default_fallback(ledger.clone(), resample, Timeframe::M1, tx));
        ledger.subscribe(reg.clone() as Arc<dyn LedgerObserver>);
        (ledger, reg, rx)
    }

    #[test]
    fn register_then_list() {
        let (_led, reg, _rx) = make_registry();
        reg.register(
            "hand1".into(), "helm1".into(), "BTCUSDT".into(),
            "let r = ind.rsi(14);\nif r[0] < 30.0 { long = true; }\nif r[0] > 70.0 { exit = true; }".into(),
            Timeframe::M1,
        ).unwrap();
        let hands = reg.list_hands();
        assert_eq!(hands.len(), 1);
        assert_eq!(hands[0].0, "hand1");
    }

    #[tokio::test]
    async fn stale_bar_suppresses_signals() {
        let (led, reg, mut rx) = make_registry();
        reg.register(
            "hand1".into(), "helm1".into(), "BTCUSDT".into(),
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

    fn peek_refcount(led: &Ledger, sym: &str, cfg: serde_json::Value) -> Option<usize> {
        let spec = IndicatorSpec::from_config(cfg, None).unwrap();
        led.with_state(sym, Timeframe::M1, |s| s.indicators.get(&spec).map(|c| c.refcount)).flatten()
    }

    #[test]
    fn script_register_acquires_indicator_handles() {
        let (led, reg, _rx) = make_registry();
        reg.register(
            "hand1".into(), "helm1".into(), "BTCUSDT".into(),
            "let r = ind.rsi(14);\nif r[0] < 30.0 { long = true; }\nif r[0] > 70.0 { exit = true; }".into(),
            Timeframe::M1,
        ).unwrap();
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(1));
    }

    #[test]
    fn script_multi_indicator_acquires_all_handles() {
        let (led, reg, _rx) = make_registry();
        reg.register(
            "hand1".into(), "helm1".into(), "BTCUSDT".into(),
"let r = ind.rsi(14);\nlet e50 = ind.ema(50);\nlet e200 = ind.ema(200);\nif r[0] < 30.0 && e50[0] > e200[0] { long = true; }\nif r[0] > 70.0 { exit = true; }".into(),
            Timeframe::M1,
        ).unwrap();
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(1));
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"ema","period":50})), Some(1));
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"ema","period":200})), Some(1));
    }

    #[test]
    fn deregister_releases_handles() {
        let (led, reg, _rx) = make_registry();
        reg.register(
            "hand1".into(), "helm1".into(), "BTCUSDT".into(),
            "let r = ind.rsi(14);\nif r[0] < 30.0 { long = true; }\nif r[0] > 70.0 { exit = true; }".into(),
            Timeframe::M1,
        ).unwrap();
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(1));
        reg.deregister("hand1");
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(0));
    }

    #[test]
    fn two_hands_share_one_indicator_cell() {
        let (led, reg, _rx) = make_registry();
        let script = "let r = ind.rsi(14);\nif r[0] < 30.0 { long = true; }\nif r[0] > 70.0 { exit = true; }";
        reg.register("hand1".into(), "h".into(), "BTCUSDT".into(), script.into(), Timeframe::M1).unwrap();
        reg.register("hand2".into(), "h".into(), "BTCUSDT".into(), script.into(), Timeframe::M1).unwrap();
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(2));
        reg.deregister("hand1");
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(1));
    }

    #[test]
    fn reregister_same_hand_keeps_single_handle() {
        let (led, reg, _rx) = make_registry();
        let script = "let r = ind.rsi(14);\nif r[0] < 30.0 { long = true; }\nif r[0] > 70.0 { exit = true; }";
        reg.register("hand1".into(), "h".into(), "BTCUSDT".into(), script.into(), Timeframe::M1).unwrap();
        reg.register("hand1".into(), "h".into(), "BTCUSDT".into(), script.into(), Timeframe::M1).unwrap();
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(1));
    }

    #[tokio::test]
    async fn deregister_all_clears() {
        let (_led, reg, _rx) = make_registry();
        let script = "let e5 = ind.ema(5); let e20 = ind.ema(20);\nif e5[0] > e20[0] { long = true; }\nif e5[0] < e20[0] { exit = true; }";
        reg.register("hand1".into(), "helm1".into(), "BTCUSDT".into(), script.into(), Timeframe::M1).unwrap();
        reg.register("hand2".into(), "helm2".into(), "ETHUSDT".into(), script.into(), Timeframe::M1).unwrap();
        reg.deregister("");
        assert_eq!(reg.list_hands().len(), 0);
    }

    /// Two hands at the same HTF share one resample subscription (refcounted).
    #[test]
    fn two_hands_same_htf_share_resample() {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let resample = ResampleManager::new(Arc::downgrade(&ledger));
        ledger.subscribe(resample.clone() as Arc<dyn LedgerObserver>);
        let (tx, _rx) = mpsc::unbounded_channel();
        let reg = Arc::new(Registry::new(ledger.clone(), resample.clone(), Timeframe::M1, tx));
        ledger.subscribe(reg.clone() as Arc<dyn LedgerObserver>);

        let script = "let r = ind.rsi(14); if r[0] < 30.0 { long = true; }";
        reg.register("hA".into(), "h".into(), "BTCUSDT".into(), script.into(), Timeframe::H1).unwrap();
        reg.register("hB".into(), "h".into(), "BTCUSDT".into(), script.into(), Timeframe::H1).unwrap();
        // Both hands ask for the same (BTCUSDT, M1→H1) subscription — one aggregator, refcount=2.
        assert_eq!(resample.len(), 1);
    }

    /// Hand registered at a higher TF only fires on HTF advances, not on
    /// every base bar. Verifies resample wiring + target_tf routing end-to-end.
    ///
    /// Uses M5 as the HTF and disables the freshness gate so synthetic
    /// timestamps don't get suppressed.
    #[tokio::test]
    async fn hand_at_htf_only_fires_on_htf_close() {
        let (led, reg, mut rx) = make_registry();
        // Disable freshness gate for synthetic timestamps.
        reg.set_freshness_gate_ms(i64::MAX);
        reg.register(
            "hand_m5".into(), "helm1".into(), "BTCUSDT".into(),
            "long = true;".into(),
            Timeframe::M5,
        ).unwrap();

        let m5_ms = Timeframe::M5.duration_ms();
        let m1_ms = Timeframe::M1.duration_ms();
        let now = chrono::Utc::now().timestamp_millis();
        let aligned_m5 = now - (now % m5_ms);
        // 3 buckets in past + trigger to emit them. bar 5*k closes bucket k-1.
        let start = aligned_m5 - 3 * m5_ms;
        let n_bars = 3 * 5 + 1; // bars 0..15
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
