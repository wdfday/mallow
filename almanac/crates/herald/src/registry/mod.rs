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
use alm_core::{Bar, Timeframe};
use alm_ledger::{AdvanceOutcome, Ledger, LedgerObserver};
use parking_lot::Mutex;
use tokio::sync::mpsc;
use tracing::{debug, trace, warn};

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
    /// The timeframe this registry evaluates at.
    tf: Timeframe,
    /// Non-stale signal batches go here; `Handler` drains and publishes them.
    signal_tx: mpsc::UnboundedSender<HandSignal>,
}

impl Registry {
    pub fn new(
        ledger: Arc<Ledger>,
        tf: Timeframe,
        signal_tx: mpsc::UnboundedSender<HandSignal>,
    ) -> Self {
        Self {
            inner: Mutex::new(RegistryInner { groups: HashMap::new(), global_config: None }),
            ledger,
            tf,
            signal_tx,
        }
    }

    /// Constructor with multiple default fallback scripts run on every symbol
    /// that has no explicit hands registered. `scripts` is evaluated in order;
    /// each gets a unique `hand_id = "default.{i}.{symbol}"`.
    pub fn with_default_scripts(
        ledger: Arc<Ledger>,
        tf: Timeframe,
        signal_tx: mpsc::UnboundedSender<HandSignal>,
        scripts: Vec<String>,
    ) -> Self {
        let r = Self::new(ledger, tf, signal_tx);
        if !scripts.is_empty() {
            r.inner.lock().global_config = Some(GlobalConfig { scripts });
        }
        r
    }

    /// Convenience constructor — single EMA-cross fallback (legacy compat).
    pub fn with_default_fallback(
        ledger: Arc<Ledger>,
        tf: Timeframe,
        signal_tx: mpsc::UnboundedSender<HandSignal>,
    ) -> Self {
        Self::with_default_scripts(ledger, tf, signal_tx, vec![
            r#"let fast = ind.ema(20); let slow = ind.ema(50);
let long = fast[1] <= slow[1] && fast[0] > slow[0];
let exit = fast[1] >= slow[1] && fast[0] < slow[0];"#.into(),
        ])
    }

    /// Register (or re-register) a hand on a symbol.
    pub fn register(
        &self,
        hand_id: String,
        helm_id: String,
        symbol: String,
        script: String,
    ) -> Result<()> {
        self.ledger.ensure_symbol(&symbol, self.tf, None);
        let hand = Handle::new(
            hand_id, helm_id, symbol.clone(), script,
            &self.ledger, self.tf,
        )?;
        debug!(
            hand_id = %hand.hand_id, symbol = %hand.symbol,
            handles = hand._indicator_handles.len(),
            "hand activated in registry"
        );
        let mut w = self.inner.lock();
        w.groups
            .entry(symbol.clone())
            .or_insert_with(|| SymbolGroup::new(symbol))
            .add(hand);
        Ok(())
    }

    /// Remove a hand by id. Cleans up empty groups. Empty `hand_id` = clear all.
    pub fn deregister(&self, hand_id: &str) {
        let mut w = self.inner.lock();
        if hand_id.is_empty() {
            w.groups.clear();
            return;
        }
        for group in w.groups.values_mut() {
            group.remove(hand_id);
        }
        w.groups.retain(|_, g| !g.is_empty());
    }

    /// Set global fallback scripts (legacy `engine.configure` compat).
    /// Replaces all existing scripts and clears registered groups.
    pub fn set_global_config(&self, script: String) {
        let mut w = self.inner.lock();
        w.groups.clear();
        w.global_config = Some(GlobalConfig { scripts: vec![script] });
    }

    /// Reset state for one symbol (or all if `symbol` is empty).
    pub fn reset(&self, symbol: &str) {
        let mut w = self.inner.lock();
        if symbol.is_empty() { w.groups.clear(); } else { w.groups.remove(symbol); }
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
        let now_ms = chrono::Utc::now().timestamp_millis();
        let age_ms = now_ms - outcome.ts;
        let is_stale = age_ms > FRESHNESS_GATE_MS;

        trace!(%symbol, bar_ts = outcome.ts, age_ms, is_stale, "evaluate_and_publish");

        let window: Vec<Bar> = self
            .ledger
            .with_state(symbol, tf, |s| s.bar_window.iter().cloned().collect())
            .unwrap_or_default();

        if window.is_empty() {
            trace!(%symbol, "window empty — skipping evaluation");
            return;
        }
        let bar = match window.last() {
            Some(b) => b.clone(),
            None => return,
        };

        {
            let r = self.inner.lock();
            if !r.groups.contains_key(symbol) {
                let needs_fallback = r.global_config.is_some();
                drop(r);
                if needs_fallback {
                    self.ensure_fallback_hand(symbol);
                } else {
                    trace!(%symbol, "no group + no fallback — skipping");
                    return;
                }
            }
        }

        let emitted = {
            let mut w = self.inner.lock();
            match w.groups.get_mut(symbol) {
                Some(g) => g.evaluate(&bar),
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
        self.ledger.ensure_symbol(symbol, self.tf, None);
        for (i, script) in scripts.iter().enumerate() {
            let hand_id = format!("default.{}.{}", i, symbol);
            match Handle::new(hand_id.clone(), String::new(), symbol.to_string(), script.clone(), &self.ledger, self.tf) {
                Ok(h) => {
                    let mut w = self.inner.lock();
                    w.groups
                        .entry(symbol.to_string())
                        .or_insert_with(|| SymbolGroup::new(symbol.to_string()))
                        .add(h);
                }
                Err(e) => warn!(%symbol, hand_id, err = %e, "failed to build fallback strategy"),
            }
        }
    }
}

impl LedgerObserver for Registry {
    fn on_advance(&self, symbol: &str, tf: Timeframe, outcome: &AdvanceOutcome) {
        if tf != self.tf { return; }
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
        let (tx, rx) = mpsc::unbounded_channel();
        let reg = Arc::new(Registry::with_default_fallback(ledger.clone(), Timeframe::M1, tx));
        ledger.subscribe(reg.clone() as Arc<dyn LedgerObserver>);
        (ledger, reg, rx)
    }

    #[test]
    fn register_then_list() {
        let (_led, reg, _rx) = make_registry();
        reg.register(
            "hand1".into(), "helm1".into(), "BTCUSDT".into(),
            "let r = ind.rsi(14);\nif r[0] < 30.0 { long = true; }\nif r[0] > 70.0 { exit = true; }".into(),
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
        ).unwrap();
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(1));
    }

    #[test]
    fn script_multi_indicator_acquires_all_handles() {
        let (led, reg, _rx) = make_registry();
        reg.register(
            "hand1".into(), "helm1".into(), "BTCUSDT".into(),
"let r = ind.rsi(14);\nlet e50 = ind.ema(50);\nlet e200 = ind.ema(200);\nif r[0] < 30.0 && e50[0] > e200[0] { long = true; }\nif r[0] > 70.0 { exit = true; }".into(),
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
        ).unwrap();
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(1));
        reg.deregister("hand1");
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(0));
    }

    #[test]
    fn two_hands_share_one_indicator_cell() {
        let (led, reg, _rx) = make_registry();
        let script = "let r = ind.rsi(14);\nif r[0] < 30.0 { long = true; }\nif r[0] > 70.0 { exit = true; }";
        reg.register("hand1".into(), "h".into(), "BTCUSDT".into(), script.into()).unwrap();
        reg.register("hand2".into(), "h".into(), "BTCUSDT".into(), script.into()).unwrap();
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(2));
        reg.deregister("hand1");
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(1));
    }

    #[test]
    fn reregister_same_hand_keeps_single_handle() {
        let (led, reg, _rx) = make_registry();
        let script = "let r = ind.rsi(14);\nif r[0] < 30.0 { long = true; }\nif r[0] > 70.0 { exit = true; }";
        reg.register("hand1".into(), "h".into(), "BTCUSDT".into(), script.into()).unwrap();
        reg.register("hand1".into(), "h".into(), "BTCUSDT".into(), script.into()).unwrap();
        assert_eq!(peek_refcount(&led, "BTCUSDT", serde_json::json!({"type":"rsi","period":14})), Some(1));
    }

    #[tokio::test]
    async fn deregister_all_clears() {
        let (_led, reg, _rx) = make_registry();
        let script = "let e5 = ind.ema(5); let e20 = ind.ema(20);\nif e5[0] > e20[0] { long = true; }\nif e5[0] < e20[0] { exit = true; }";
        reg.register("hand1".into(), "helm1".into(), "BTCUSDT".into(), script.into()).unwrap();
        reg.register("hand2".into(), "helm2".into(), "ETHUSDT".into(), script.into()).unwrap();
        reg.deregister("");
        assert_eq!(reg.list_hands().len(), 0);
    }
}
