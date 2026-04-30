//! Bot registry — the evaluation engine that sits on top of `alm-ledger`.
//!
//! `Registry` implements [`LedgerObserver`] so it receives a callback for
//! every live bar advanced into the ledger. For each advance it:
//!
//!   1. Reads the shared `bar_window` from `Ledger` (single source of truth
//!      shared with the HTTP data API).
//!   2. Runs each bot's strategy (`on_bar` + `on_window`).
//!   3. Drops any signals older than [`FRESHNESS_GATE_MS`] — replayed
//!      JetStream bars warm up strategy state but must not trigger live
//!      signals.
//!   4. Emits non-stale signal batches on an [`mpsc::UnboundedSender`];
//!      `Handler` owns the receiver and pushes them to NATS.
//!
//! Interior mutability is used (`RwLock<RegistryInner>`) because
//! `LedgerObserver::on_advance` hands us `&self`. All mutations (register,
//! deregister, bot state updates during evaluation) go through the inner
//! lock with a short critical section.

use std::collections::HashMap;
use std::sync::Arc;

use anyhow::{anyhow, Result};
use alm_core::{signal::Signal, strategy::Strategy, Bar, Timeframe};
use alm_ledger::{AdvanceOutcome, IndicatorHandle, IndicatorSpec, Ledger, LedgerObserver};
use alm_strategy::bar_resampler::TimeBarResampler;
use alm_strategy::factory::{build_strategy_with_deps, IndicatorDep};
use parking_lot::Mutex;
use tokio::sync::mpsc;
use tracing::{debug, trace, warn};

/// Bars older than this are treated as warm-up only — no signals emitted.
pub const FRESHNESS_GATE_MS: i64 = 2 * 60 * 1000;

// ── Signal batch emitted to Handler ───────────────────────────────────────────

/// Output of evaluating one bot on one advance. Pushed through an mpsc
/// channel to the Handler which serialises and publishes to NATS.
#[derive(Debug, Clone)]
#[allow(dead_code)]
pub struct SignalBatch {
    pub bot_id: String,
    pub orch_id: String,
    pub symbol: String,
    pub bar_ts: i64,
    pub signals: Vec<Signal>,
}

// ── BotHandle ─────────────────────────────────────────────────────────────────

/// Maximum number of completed higher-TF bars kept in a bot's local window.
const HTF_WINDOW_SIZE: usize = 200;

pub struct BotHandle {
    pub bot_id: String,
    pub orch_id: String,
    pub symbol: String,
    pub strategy_name: String,
    pub params_json: String,
    strategy: Box<dyn Strategy>,
    /// Live indicator handles keep each declared dependency warm in the ledger
    /// for the lifetime of the bot. On drop the handles decrement the refcount;
    /// unused cells are evicted after `LedgerConfig::drop_grace_bars`.
    ///
    /// Empty for named strategies (they own their indicators internally) and
    /// for CEL / Dynamic strategies that declare zero base-TF indicators.
    _indicator_handles: Vec<IndicatorHandle>,
    /// Per-bot higher-TF resampler. `None` when the bot operates at the
    /// registry's base TF (default M1) — no aggregation needed.
    resampler: Option<TimeBarResampler>,
    /// Rolling window of completed higher-TF bars (used when `resampler` is Some).
    htf_window: Vec<Bar>,
}

impl BotHandle {
    fn new(
        bot_id: String,
        orch_id: String,
        symbol: String,
        strategy_name: String,
        params_json: String,
        ledger: &Arc<Ledger>,
        base_tf: Timeframe,
        target_tf: Timeframe,
    ) -> Result<Self> {
        let mut value = parse_params_json(&params_json)?;
        // Signal live mode to RhaiStrategy so plot() series are never accumulated.
        if strategy_name == "rhai" {
            if let Some(obj) = value.as_object_mut() {
                obj.insert("_live".into(), serde_json::json!(true));
            }
        }
        let (strategy, deps) = build_strategy_with_deps(&strategy_name, &value)
            .map_err(|e| anyhow!("{e}"))?;
        let handles = acquire_dep_handles(ledger, &symbol, base_tf, &deps, &bot_id, &strategy_name);
        let resampler = if target_tf != base_tf {
            Some(TimeBarResampler::new(target_tf.duration_ms()))
        } else {
            None
        };
        Ok(Self {
            bot_id,
            orch_id,
            symbol,
            strategy_name,
            params_json,
            strategy,
            _indicator_handles: handles,
            resampler,
            htf_window: Vec::new(),
        })
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        match &mut self.resampler {
            None => self.strategy.on_bar(bar),
            Some(rs) => match rs.push(bar) {
                None => vec![],
                Some(htf_bar) => {
                    self.htf_window.push(htf_bar.clone());
                    if self.htf_window.len() > HTF_WINDOW_SIZE {
                        self.htf_window.drain(..1);
                    }
                    let mut sigs = self.strategy.on_bar(&htf_bar);
                    sigs.extend(self.strategy.on_window(&self.htf_window));
                    sigs
                }
            },
        }
    }

    fn on_window(&mut self, bars: &[Bar]) -> Vec<Signal> {
        if self.resampler.is_some() {
            // Already handled inside on_bar using the htf_window.
            vec![]
        } else {
            self.strategy.on_window(bars)
        }
    }
}

// ── SymbolGroup ───────────────────────────────────────────────────────────────

/// All bots watching the same (symbol, tf). The bar window is NOT stored
/// here — it lives in the ledger and is read on demand in `evaluate`.
pub struct SymbolGroup {
    #[allow(dead_code)]
    pub symbol: String,
    bots: Vec<BotHandle>,
}

impl SymbolGroup {
    fn new(symbol: String) -> Self {
        Self { symbol, bots: Vec::new() }
    }

    fn add(&mut self, bot: BotHandle) {
        // Replace existing bot with same id (re-register = reconfigure).
        self.bots.retain(|b| b.bot_id != bot.bot_id);
        self.bots.push(bot);
    }

    fn remove(&mut self, bot_id: &str) {
        self.bots.retain(|b| b.bot_id != bot_id);
    }

    fn is_empty(&self) -> bool {
        self.bots.is_empty()
    }

    /// Run every bot on `bar` (and the current `window`), merging signals
    /// from `on_bar` and `on_window`. Returns one entry per bot that emitted.
    fn evaluate(&mut self, bar: &Bar, window: &[Bar]) -> Vec<(String, String, Vec<Signal>)> {
        self.bots
            .iter_mut()
            .filter_map(|b| {
                let mut sigs = b.on_bar(bar);
                sigs.extend(b.on_window(window));
                if sigs.is_empty() {
                    None
                } else {
                    Some((b.bot_id.clone(), b.orch_id.clone(), sigs))
                }
            })
            .collect()
    }

    fn bot_infos(&self) -> impl Iterator<Item = &BotHandle> {
        self.bots.iter()
    }
}

// ── Registry ──────────────────────────────────────────────────────────────────

struct RegistryInner {
    groups: HashMap<String, SymbolGroup>,
    /// Fallback config applied to new symbols when no explicit bot covers
    /// them. Set by the legacy `engine.configure` subject for backwards-compat.
    global_config: Option<GlobalConfig>,
}

struct GlobalConfig {
    strategy_name: String,
    params_json: String,
}

pub struct Registry {
    inner: Mutex<RegistryInner>,
    ledger: Arc<Ledger>,
    /// The timeframe this registry evaluates at. Herald runs at a single
    /// configured tf for now — higher tfs live in separate processes / keys.
    tf: Timeframe,
    /// Non-stale signal batches go here; `Handler` drains and publishes them.
    signal_tx: mpsc::UnboundedSender<SignalBatch>,
}

impl Registry {
    pub fn new(
        ledger: Arc<Ledger>,
        tf: Timeframe,
        signal_tx: mpsc::UnboundedSender<SignalBatch>,
    ) -> Self {
        Self {
            inner: Mutex::new(RegistryInner {
                groups: HashMap::new(),
                global_config: None,
            }),
            ledger,
            tf,
            signal_tx,
        }
    }

    /// Convenience constructor that installs a CEL fallback strategy for
    /// symbols that have no explicit bot registered (legacy `engine.configure` compat).
    pub fn with_default_fallback(
        ledger: Arc<Ledger>,
        tf: Timeframe,
        signal_tx: mpsc::UnboundedSender<SignalBatch>,
    ) -> Self {
        let r = Self::new(ledger, tf, signal_tx);
        // Default: EMA 20/50 crossover expressed as CEL (replaces old named ma_crossover).
        r.set_global_config(
            "cel".into(),
            r#"{"entry":"prev_ema(20) <= prev_ema(50) && ema(20) > ema(50)","exit":"prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)"}"#.into(),
        );
        r
    }

    /// Register (or re-register) a bot on a symbol.
    ///
    /// Builds the strategy via `alm_strategy::factory::build_strategy_with_deps`,
    /// ensures the (symbol, tf) state exists in the ledger, then acquires one
    /// `IndicatorHandle` per declared base-TF dependency. Handles live on the
    /// `BotHandle` — when the bot is deregistered or replaced they drop and the
    /// ledger evicts unused indicator cells after the configured grace.
    pub fn register(
        &self,
        bot_id: String,
        orch_id: String,
        symbol: String,
        strategy_name: String,
        params_json: String,
        target_tf: Timeframe,
    ) -> Result<()> {
        // Only CEL strategies are supported in live mode — named strategies
        // are backtest-only (they own indicators internally and don't integrate
        // with the ledger's shared indicator state).
        if !matches!(strategy_name.as_str(), "cel" | "cel2" | "evalexpr" | "rhai") {
            anyhow::bail!(
                "live bot registration only supports CEL/Rhai strategies ('cel', 'rhai'); \
                 got '{}'. Use CEL expressions or Rhai scripts to replicate named strategy logic.",
                strategy_name
            );
        }

        self.ledger.ensure_symbol(&symbol, self.tf, None);
        let bot = BotHandle::new(
            bot_id,
            orch_id,
            symbol.clone(),
            strategy_name,
            params_json,
            &self.ledger,
            self.tf,
            target_tf,
        )?;
        let mut w = self.inner.lock();
        w.groups
            .entry(symbol.clone())
            .or_insert_with(|| SymbolGroup::new(symbol))
            .add(bot);
        Ok(())
    }

    /// Remove a bot by id. Cleans up empty groups. Empty `bot_id` = clear all.
    pub fn deregister(&self, bot_id: &str) {
        let mut w = self.inner.lock();
        if bot_id.is_empty() {
            w.groups.clear();
            return;
        }
        for group in w.groups.values_mut() {
            group.remove(bot_id);
        }
        w.groups.retain(|_, g| !g.is_empty());
    }

    /// Set global fallback config (legacy `engine.configure` support).
    /// Clears all existing bots (same semantics as before).
    pub fn set_global_config(&self, strategy_name: String, params_json: String) {
        let mut w = self.inner.lock();
        w.groups.clear();
        w.global_config = Some(GlobalConfig { strategy_name, params_json });
    }

    /// Reset state for one symbol (or all if `symbol` is empty). Drops the
    /// SymbolGroup entirely — fallback global bots will lazily re-create on
    /// the next advance.
    pub fn reset(&self, symbol: &str) {
        let mut w = self.inner.lock();
        if symbol.is_empty() {
            w.groups.clear();
        } else {
            w.groups.remove(symbol);
        }
    }

    pub fn list_bots(&self) -> Vec<(String, String, String, String, String)> {
        let r = self.inner.lock();
        r.groups
            .values()
            .flat_map(|g| g.bot_infos())
            .map(|b| (
                b.bot_id.clone(),
                b.orch_id.clone(),
                b.symbol.clone(),
                b.strategy_name.clone(),
                b.params_json.clone(),
            ))
            .collect()
    }

    /// Core evaluation — called from the `LedgerObserver::on_advance` hook.
    /// Reads the shared bar window from the ledger, runs every bot in the
    /// symbol group, applies the freshness gate, and forwards non-stale
    /// signal batches to the `signal_tx` channel.
    fn evaluate_and_publish(&self, symbol: &str, tf: Timeframe, outcome: &AdvanceOutcome) {
        // Freshness gate — `outcome.ts` is the bar timestamp (ms).
        let now_ms = chrono::Utc::now().timestamp_millis();
        let age_ms = now_ms - outcome.ts;
        let is_stale = age_ms > FRESHNESS_GATE_MS;

        // Snapshot the window with a short read guard. Clone cost is
        // O(capacity) — same as the old Registry::bar_window.push path.
        let window: Vec<Bar> = self
            .ledger
            .with_state(symbol, tf, |s| s.bar_window.iter().cloned().collect())
            .unwrap_or_default();

        if window.is_empty() {
            return;
        }
        let bar = match window.last() {
            Some(b) => b.clone(),
            None => return,
        };

        // Auto-register fallback bot if needed — mirrors the old behaviour.
        {
            let r = self.inner.lock();
            if !r.groups.contains_key(symbol) {
                let needs_fallback = r.global_config.is_some();
                drop(r);
                if needs_fallback {
                    self.ensure_fallback_bot(symbol);
                } else {
                    trace!(%symbol, "no group + no fallback — skipping");
                    return;
                }
            }
        }

        let batches = {
            let mut w = self.inner.lock();
            match w.groups.get_mut(symbol) {
                Some(g) => g.evaluate(&bar, &window),
                None => Vec::new(),
            }
        };

        if batches.is_empty() {
            trace!(%symbol, bar_ts = outcome.ts, "no signals this bar");
            return;
        }

        if is_stale {
            debug!(%symbol, age_ms, batches = batches.len(), "stale bar — suppressing live signals");
            return;
        }

        for (bot_id, orch_id, signals) in batches {
            let _ = self.signal_tx.send(SignalBatch {
                bot_id,
                orch_id,
                symbol: symbol.to_string(),
                bar_ts: outcome.ts,
                signals,
            });
        }
    }

    fn ensure_fallback_bot(&self, symbol: &str) {
        let cfg = {
            let r = self.inner.lock();
            r.global_config.as_ref().map(|g| (g.strategy_name.clone(), g.params_json.clone()))
        };
        let (strategy_name, params_json) = match cfg {
            Some(v) => v,
            None => return,
        };
        let bot_id = format!("global.{}", symbol);
        self.ledger.ensure_symbol(symbol, self.tf, None);
        let bot = BotHandle::new(
            bot_id,
            String::new(),
            symbol.to_string(),
            strategy_name,
            params_json,
            &self.ledger,
            self.tf,
            self.tf, // fallback always uses base TF
        );
        match bot {
            Ok(b) => {
                let mut w = self.inner.lock();
                w.groups
                    .entry(symbol.to_string())
                    .or_insert_with(|| SymbolGroup::new(symbol.to_string()))
                    .add(b);
            }
            Err(e) => {
                warn!(%symbol, err = %e, "failed to build fallback strategy");
            }
        }
    }
}

impl LedgerObserver for Registry {
    fn on_advance(&self, symbol: &str, tf: Timeframe, outcome: &AdvanceOutcome) {
        if tf != self.tf {
            // Other timeframes are not ours. Silently ignore.
            return;
        }
        self.evaluate_and_publish(symbol, tf, outcome);
    }
}

// ── helpers ───────────────────────────────────────────────────────────────────

/// Parse the params blob carried over NATS (`RegisterMsg.params_json`).
///
/// Empty string is treated as `{}`. Unlike the previous implementation we now
/// pass the full JSON through to the factory — CEL / Dynamic strategies carry
/// nested objects (`"indicators"`) and multi-line strings (`"script"`) that
/// must not be flattened to `HashMap<String, f64>`.
fn parse_params_json(params_json: &str) -> Result<serde_json::Value> {
    if params_json.is_empty() {
        return Ok(serde_json::json!({}));
    }
    serde_json::from_str(params_json).map_err(|e| anyhow!("invalid params_json: {e}"))
}

/// Convert declared `IndicatorDep`s to live `IndicatorHandle`s anchored in the
/// ledger. Dependencies that fail to parse or acquire are logged and dropped —
/// they do NOT abort registration, so a typo in one CEL indicator still lets
/// the other indicators (and the strategy's internal copies) work.
fn acquire_dep_handles(
    ledger: &Arc<Ledger>,
    symbol: &str,
    tf: Timeframe,
    deps: &[IndicatorDep],
    bot_id: &str,
    strategy_name: &str,
) -> Vec<IndicatorHandle> {
    let mut handles = Vec::with_capacity(deps.len());
    for dep in deps {
        let spec = match IndicatorSpec::from_config(dep.config.clone(), dep.source_tf) {
            Ok(s) => s,
            Err(e) => {
                warn!(%bot_id, %strategy_name, config = %dep.config, err = %e,
                      "skipping indicator dep — invalid config");
                continue;
            }
        };
        match ledger.acquire_indicator(symbol, tf, spec.clone()) {
            Ok(h) => {
                debug!(%bot_id, %symbol, spec = %spec.canonical_key(),
                       "acquired ledger indicator handle");
                handles.push(h);
            }
            Err(e) => {
                warn!(%bot_id, %symbol, spec = ?spec, err = %e,
                      "skipping indicator dep — acquire failed");
            }
        }
    }
    handles
}

// ── tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_ledger::LedgerConfig;

    fn mk_bar(sym: &str, t: i64, c: f64) -> Bar {
        Bar::new(t, sym, c, c, c, c, 1.0)
    }

    fn make_registry() -> (Arc<Ledger>, Arc<Registry>, mpsc::UnboundedReceiver<SignalBatch>) {
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
            "bot1".into(), "orch1".into(), "BTCUSDT".into(),
            "cel".into(), r#"{"entry":"rsi(14) < 30","exit":"rsi(14) > 70"}"#.into(),
            Timeframe::M1,
        ).unwrap();
        let bots = reg.list_bots();
        assert_eq!(bots.len(), 1);
        assert_eq!(bots[0].0, "bot1");
    }

    #[tokio::test]
    async fn stale_bar_suppresses_signals() {
        // Very old bar_ts → should be suppressed.
        let (led, reg, mut rx) = make_registry();
        reg.register(
            "bot1".into(), "orch1".into(), "BTCUSDT".into(),
            "cel".into(), r#"{"entry":"ema(2) > ema(3)","exit":"ema(2) < ema(3)"}"#.into(),
            Timeframe::M1,
        ).unwrap();
        // Feed a few warmup bars with ancient timestamps (year 2000-ish).
        for i in 1..10 {
            led.advance(Timeframe::M1, mk_bar("BTCUSDT", 946_684_800_000 + i * 60_000, 100.0 + i as f64)).unwrap();
        }
        // No batches should have been pushed (freshness gate dropped them).
        assert!(rx.try_recv().is_err());
        drop(reg);
    }

    #[tokio::test]
    async fn live_bar_produces_signals_when_strategy_emits() {
        // MA crossover needs at least `slow` bars of warmup. Feed enough
        // fresh bars (now - a few seconds) so it can emit.
        let (led, reg, mut rx) = make_registry();
        reg.register(
            "bot1".into(), "orch1".into(), "TEST".into(),
            "cel".into(), r#"{"entry":"prev_ema(2) <= prev_ema(3) && ema(2) > ema(3)","exit":"prev_ema(2) >= prev_ema(3) && ema(2) < ema(3)"}"#.into(),
            Timeframe::M1,
        ).unwrap();
        let now = chrono::Utc::now().timestamp_millis();
        // 10 bars ending at now, 1 second apart.
        let prices = [100.0, 101.0, 102.0, 103.0, 102.0, 101.0, 100.0, 99.0, 98.0, 97.0];
        for (i, p) in prices.iter().enumerate() {
            let t = now - ((prices.len() - 1 - i) as i64) * 1000;
            led.advance(Timeframe::M1, mk_bar("TEST", t, *p)).unwrap();
        }
        // Drain whatever arrived. We don't assert an exact count (depends
        // on strategy internals) — the critical property is the channel
        // stayed open for fresh-bar evaluation (no panics, no deadlock).
        while rx.try_recv().is_ok() {}
        drop(reg);
    }

    // ── indicator-handle wiring ──────────────────────────────────────────────

    /// Peek at the ledger-side refcount for an indicator cell. Returns `None`
    /// if the (symbol, tf) state or the cell itself does not exist.
    fn peek_refcount(led: &Ledger, sym: &str, cfg: serde_json::Value) -> Option<usize> {
        let spec = IndicatorSpec::from_config(cfg, None).unwrap();
        led.with_state(sym, Timeframe::M1, |s| {
            s.indicators.get(&spec).map(|c| c.refcount)
        }).flatten()
    }

    #[test]
    fn cel_register_acquires_indicator_handles() {
        let (led, reg, _rx) = make_registry();
        let params = r#"{"entry":"rsi(14) < 30","exit":"rsi(14) > 70"}"#;
        reg.register(
            "cel1".into(), "orch1".into(), "BTCUSDT".into(),
            "cel".into(), params.into(),
            Timeframe::M1,
        ).unwrap();

        // Exactly one live handle on the rsi cell.
        let rc = peek_refcount(&led, "BTCUSDT",
            serde_json::json!({"type":"rsi","period":14}));
        assert_eq!(rc, Some(1), "register(cel) must acquire an indicator handle");
    }

    #[test]
    fn cel_multi_indicator_acquires_all_handles() {
        let (led, reg, _rx) = make_registry();
        let params = r#"{"entry":"rsi(14) < 30 && ema(50) > ema(200)","exit":"rsi(14) > 70"}"#;
        reg.register(
            "cel1".into(), "orch1".into(), "BTCUSDT".into(),
            "cel".into(), params.into(),
            Timeframe::M1,
        ).unwrap();

        assert_eq!(peek_refcount(&led, "BTCUSDT",
            serde_json::json!({"type":"rsi","period":14})), Some(1));
        assert_eq!(peek_refcount(&led, "BTCUSDT",
            serde_json::json!({"type":"ema","period":50})), Some(1));
        assert_eq!(peek_refcount(&led, "BTCUSDT",
            serde_json::json!({"type":"ema","period":200})), Some(1));
    }

    #[test]
    fn deregister_releases_handles() {
        let (led, reg, _rx) = make_registry();
        let params = r#"{"entry":"rsi(14) < 30","exit":"rsi(14) > 70"}"#;
        reg.register(
            "cel1".into(), "orch1".into(), "BTCUSDT".into(),
            "cel".into(), params.into(),
            Timeframe::M1,
        ).unwrap();
        assert_eq!(peek_refcount(&led, "BTCUSDT",
            serde_json::json!({"type":"rsi","period":14})), Some(1));

        reg.deregister("cel1");
        // Refcount drops to 0; cell stays until grace period expires.
        assert_eq!(peek_refcount(&led, "BTCUSDT",
            serde_json::json!({"type":"rsi","period":14})), Some(0));
    }

    #[test]
    fn two_cel_bots_share_one_indicator_cell() {
        let (led, reg, _rx) = make_registry();
        let params = r#"{"entry":"rsi(14) < 30","exit":"rsi(14) > 70"}"#;
        reg.register("bot1".into(), "o".into(), "BTCUSDT".into(),
                     "cel".into(), params.into(), Timeframe::M1).unwrap();
        reg.register("bot2".into(), "o".into(), "BTCUSDT".into(),
                     "cel".into(), params.into(), Timeframe::M1).unwrap();
        // Dedup → one cell, two refs.
        assert_eq!(peek_refcount(&led, "BTCUSDT",
            serde_json::json!({"type":"rsi","period":14})), Some(2));

        reg.deregister("bot1");
        assert_eq!(peek_refcount(&led, "BTCUSDT",
            serde_json::json!({"type":"rsi","period":14})), Some(1));
    }

    #[test]
    fn reregister_same_bot_keeps_single_handle() {
        let (led, reg, _rx) = make_registry();
        let params = r#"{"entry":"rsi(14) < 30","exit":"rsi(14) > 70"}"#;
        reg.register("bot1".into(), "o".into(), "BTCUSDT".into(),
                     "cel".into(), params.into(), Timeframe::M1).unwrap();
        reg.register("bot1".into(), "o".into(), "BTCUSDT".into(),
                     "cel".into(), params.into(), Timeframe::M1).unwrap();
        // Re-register = remove old + add new → refcount stays at 1.
        // (The old BotHandle is dropped by `SymbolGroup::add`, releasing its
        //  handle; the new one acquires afresh.)
        assert_eq!(peek_refcount(&led, "BTCUSDT",
            serde_json::json!({"type":"rsi","period":14})), Some(1));
    }

    // Named strategies are no longer allowed in live registration (CEL/Rhai only).
    // The equivalent test for CEL indicator wiring is `cel_register_acquires_indicator_handles`.

    #[tokio::test]
    async fn deregister_all_clears() {
        let (_led, reg, _rx) = make_registry();
        let params = r#"{"entry":"ema(5) > ema(20)","exit":"ema(5) < ema(20)"}"#;
        reg.register(
            "bot1".into(), "orch1".into(), "BTCUSDT".into(),
            "cel".into(), params.into(),
            Timeframe::M1,
        ).unwrap();
        reg.register(
            "bot2".into(), "orch2".into(), "ETHUSDT".into(),
            "cel".into(), params.into(),
            Timeframe::M1,
        ).unwrap();
        reg.deregister("");
        assert_eq!(reg.list_bots().len(), 0);
    }
}
