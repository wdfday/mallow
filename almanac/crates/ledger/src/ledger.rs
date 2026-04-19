//! `Ledger` — the live market-state container shared between the bar
//! ingestion path, the bot registry, and the herald HTTP API.
//!
//! Concurrency model:
//! - `DashMap<(Symbol, Timeframe), Arc<RwLock<SymbolState>>>` — sharded,
//!   so different symbols never contend.
//! - Writers (bar ingestion, `ensure_indicator`) take the per-state
//!   `RwLock` write guard for the short window needed to mutate.
//! - Readers (registry evaluate, HTTP handlers) take the read guard and
//!   usually materialise a `SymbolSnapshot` before releasing it.

use std::collections::HashMap;
use std::sync::Arc;

use alm_core::{Bar, Timeframe};
use dashmap::DashMap;
use parking_lot::RwLock;
use serde::{Deserialize, Serialize};
use tracing::{debug, info, trace, warn};

use crate::handle::IndicatorHandle;
use crate::spec::IndicatorSpec;
use crate::state::{AdvanceOutcome, SymbolSnapshot, SymbolState};

pub type SymbolKey = (String, Timeframe);

/// Static configuration for the ledger — typically wired from env / YAML
/// once at startup. Per-TF window sizes can be tuned independently
/// (crypto 24/7 ≠ stocks) and auto-expand when large-period indicators
/// are registered.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LedgerConfig {
    /// Default window size per timeframe. Falls back to `fallback_window`
    /// when a TF is not listed.
    pub default_window: HashMap<Timeframe, usize>,
    /// Used when `default_window` has no entry for the requested TF.
    pub fallback_window: usize,
    /// `capacity >= max(default, window_multiplier * longest_period + display_tail)`.
    pub window_multiplier: usize,
    /// Extra tail added on top of the multiplier-based minimum so that
    /// recent history remains visible after a big indicator is registered.
    pub display_tail: usize,
    /// Hard cap on the number of distinct `IndicatorSpec`s a single
    /// (symbol, tf) may host — protects memory against pathological clients.
    pub max_indicators_per_symbol: usize,
    /// Reject `acquire_indicator` / `pin_indicator` when
    /// `spec.longest_period() > max_period`.
    pub max_period: usize,
    /// Number of bars an unreferenced indicator is retained before
    /// eviction. Re-acquiring within this window reuses the existing
    /// warmup. 0 = evict immediately on refcount→0.
    pub drop_grace_bars: u64,
}

impl Default for LedgerConfig {
    fn default() -> Self {
        let mut default_window = HashMap::new();
        default_window.insert(Timeframe::M1,  1000);
        default_window.insert(Timeframe::M5,  1000);
        default_window.insert(Timeframe::M15, 1000);
        default_window.insert(Timeframe::H1,  1000);
        default_window.insert(Timeframe::H4,   500);
        default_window.insert(Timeframe::D1,   252);
        default_window.insert(Timeframe::W1,   260);
        Self {
            default_window,
            fallback_window: 1000,
            window_multiplier: 5,
            display_tail: 100,
            max_indicators_per_symbol: 64,
            max_period: 5000,
            drop_grace_bars: 300,
        }
    }
}

impl LedgerConfig {
    pub fn default_window_for(&self, tf: Timeframe) -> usize {
        *self.default_window.get(&tf).unwrap_or(&self.fallback_window)
    }

    /// Required capacity given a longest indicator period.
    pub fn required_capacity(&self, tf: Timeframe, longest_period: usize) -> usize {
        let base = self.default_window_for(tf);
        let period_based = longest_period
            .saturating_mul(self.window_multiplier)
            .saturating_add(self.display_tail);
        base.max(period_based)
    }
}

/// Observer of `Ledger::advance` events. The registry subscribes to this
/// trait so that strategies get notified exactly once per ingested bar
/// without polling.
pub trait LedgerObserver: Send + Sync {
    /// Called after a successful advance. `outcome.skipped` is always false
    /// for this callback — skipped bars do not fan out.
    fn on_advance(&self, symbol: &str, tf: Timeframe, outcome: &AdvanceOutcome);
}

/// Central state machine. Owns all `SymbolState`s and fans out advance events.
pub struct Ledger {
    cfg: LedgerConfig,
    states: DashMap<SymbolKey, Arc<RwLock<SymbolState>>>,
    observers: RwLock<Vec<Arc<dyn LedgerObserver>>>,
}

impl Ledger {
    pub fn new(cfg: LedgerConfig) -> Self {
        Self { cfg, states: DashMap::new(), observers: RwLock::new(Vec::new()) }
    }

    /// View the active config (read-only).
    pub fn config(&self) -> &LedgerConfig {
        &self.cfg
    }

    /// Register an observer. Cheap — observers are rarely added/removed.
    pub fn subscribe(&self, observer: Arc<dyn LedgerObserver>) {
        self.observers.write().push(observer);
    }

    /// Ensure a `SymbolState` exists for this `(symbol, tf)` with the
    /// given capacity (grows but never shrinks). Returns the Arc for
    /// subsequent locking.
    pub fn ensure_symbol(
        &self,
        symbol: &str,
        tf: Timeframe,
        wanted_capacity: Option<usize>,
    ) -> Arc<RwLock<SymbolState>> {
        let key = (symbol.to_string(), tf);
        let mut created = false;
        let entry = self.states.entry(key).or_insert_with(|| {
            let cap = wanted_capacity.unwrap_or_else(|| self.cfg.default_window_for(tf));
            created = true;
            debug!(symbol, ?tf, capacity = cap, "ensure_symbol: created new state");
            Arc::new(RwLock::new(SymbolState::new(symbol, tf, cap)))
        });
        let arc = entry.clone();
        drop(entry);
        if let Some(c) = wanted_capacity {
            let mut w = arc.write();
            let old = w.capacity;
            w.ensure_capacity(c);
            if !created && c > old {
                debug!(symbol, ?tf, old_cap = old, new_cap = w.capacity, "expanded symbol capacity");
            }
        }
        arc
    }

    /// Look up an existing state (no creation). Returns `None` if unknown.
    pub fn state(&self, symbol: &str, tf: Timeframe) -> Option<Arc<RwLock<SymbolState>>> {
        self.states.get(&(symbol.to_string(), tf)).map(|r| r.clone())
    }

    /// All (symbol, tf) pairs currently tracked.
    pub fn keys(&self) -> Vec<SymbolKey> {
        self.states.iter().map(|r| r.key().clone()).collect()
    }

    /// Ensure a cell for `spec` exists under `(symbol, tf)` and warms it
    /// up against the current bar window. Does **not** touch the
    /// refcount — use [`Self::acquire_indicator`] or [`Self::pin_indicator`]
    /// for the public API.
    ///
    /// Errors:
    /// - spec exceeds `cfg.max_period`
    /// - (symbol, tf) already has `cfg.max_indicators_per_symbol` indicators
    fn ensure_cell_for(
        &self,
        symbol: &str,
        tf: Timeframe,
        spec: &IndicatorSpec,
    ) -> anyhow::Result<()> {
        let period = spec.longest_period();
        if period > self.cfg.max_period {
            warn!(
                symbol,
                ?tf,
                spec = %spec,
                period,
                max_period = self.cfg.max_period,
                "rejecting indicator: period exceeds max_period",
            );
            anyhow::bail!(
                "indicator {} exceeds max_period {} (period {})",
                spec.canonical_key(),
                self.cfg.max_period,
                period,
            );
        }
        let required_cap = self.cfg.required_capacity(tf, period);
        let arc = self.ensure_symbol(symbol, tf, Some(required_cap));
        let mut w = arc.write();
        if w.indicators.contains_key(spec) {
            trace!(symbol, ?tf, spec = %spec, "ensure_cell_for: dedup hit");
            return Ok(());
        }
        if w.indicators.len() >= self.cfg.max_indicators_per_symbol {
            warn!(
                symbol,
                ?tf,
                spec = %spec,
                registered = w.indicators.len(),
                cap = self.cfg.max_indicators_per_symbol,
                "rejecting indicator: per-symbol cap reached",
            );
            anyhow::bail!(
                "(symbol={}, tf={}) reached indicator cap {}",
                symbol, tf, self.cfg.max_indicators_per_symbol,
            );
        }
        w.ensure_capacity(required_cap);
        w.ensure_indicator(spec.clone())?;
        info!(
            symbol,
            ?tf,
            spec = %spec,
            period,
            capacity = w.capacity,
            total_indicators = w.indicators.len(),
            "registered indicator",
        );
        Ok(())
    }

    /// Acquire a live subscription to an indicator. Returns an
    /// [`IndicatorHandle`] whose drop decrements the per-cell refcount;
    /// when refcount reaches 0, the cell enters a grace period (see
    /// [`LedgerConfig::drop_grace_bars`]) and is evicted later unless
    /// re-acquired.
    ///
    /// Idempotent at the spec level — acquiring the same spec twice
    /// yields two handles sharing one cell.
    pub fn acquire_indicator(
        self: &Arc<Self>,
        symbol: &str,
        tf: Timeframe,
        spec: IndicatorSpec,
    ) -> anyhow::Result<IndicatorHandle> {
        self.ensure_cell_for(symbol, tf, &spec)?;
        // ensure_cell_for succeeded → state exists and contains the spec.
        let arc = self.state(symbol, tf).expect("state must exist after ensure_cell_for");
        let known = {
            let mut w = arc.write();
            w.retain_indicator(&spec)
        };
        debug_assert!(known, "retain_indicator missed a cell just created by ensure_cell_for");
        let symbol_arc: Arc<str> = Arc::from(symbol);
        Ok(IndicatorHandle::new(self, symbol_arc, tf, spec, false))
    }

    /// Acquire several indicators; short-circuits on the first error.
    /// All handles created *before* the error are returned to the
    /// caller in the `Err` variant so they can be dropped cleanly.
    pub fn acquire_indicators(
        self: &Arc<Self>,
        symbol: &str,
        tf: Timeframe,
        specs: impl IntoIterator<Item = IndicatorSpec>,
    ) -> anyhow::Result<Vec<IndicatorHandle>> {
        let mut handles = Vec::new();
        for s in specs {
            match self.acquire_indicator(symbol, tf, s) {
                Ok(h) => handles.push(h),
                Err(e) => {
                    drop(handles); // release what we got
                    return Err(e);
                }
            }
        }
        Ok(handles)
    }

    /// Register an indicator as **pinned** — never evicted for the
    /// lifetime of the ledger. Used by the warm-set and other "always
    /// available" overlays. Idempotent; re-pinning is a no-op.
    ///
    /// No handle is returned — pinned cells do not participate in the
    /// refcount machinery.
    pub fn pin_indicator(
        self: &Arc<Self>,
        symbol: &str,
        tf: Timeframe,
        spec: IndicatorSpec,
    ) -> anyhow::Result<()> {
        self.ensure_cell_for(symbol, tf, &spec)?;
        let arc = self.state(symbol, tf).expect("state must exist after ensure_cell_for");
        let mut w = arc.write();
        w.pin_indicator(&spec);
        Ok(())
    }

    /// Internal: called by `IndicatorHandle::drop`. Decrements refcount
    /// on the cell; when it reaches 0, schedules grace eviction.
    pub(crate) fn release_indicator(&self, symbol: &str, tf: Timeframe, spec: &IndicatorSpec) {
        let Some(arc) = self.state(symbol, tf) else {
            trace!(symbol, ?tf, spec = %spec, "release_indicator: state not found");
            return;
        };
        let mut w = arc.write();
        w.release_indicator(spec, self.cfg.drop_grace_bars);
    }

    /// Register the warm-set for one (symbol, tf). Best-effort: specs
    /// that are rejected (e.g. `max_indicators_per_symbol` reached) are
    /// logged and skipped, but the rest proceed. All successful specs
    /// are **pinned** (immortal) so warm-set cells never enter the
    /// eviction path. Returns `(registered, skipped)`.
    pub fn apply_warm_set(
        self: &Arc<Self>,
        symbol: &str,
        tf: Timeframe,
        specs: impl IntoIterator<Item = IndicatorSpec>,
    ) -> (usize, usize) {
        let mut ok = 0usize;
        let mut bad = 0usize;
        for s in specs {
            let key = s.canonical_key();
            match self.pin_indicator(symbol, tf, s) {
                Ok(()) => ok += 1,
                Err(e) => {
                    bad += 1;
                    warn!(symbol, ?tf, spec = %key, err = %e, "warm-set spec rejected");
                }
            }
        }
        info!(symbol, ?tf, registered = ok, skipped = bad, "warm-set applied (pinned)");
        (ok, bad)
    }

    /// Advance a bar. Symbol/tf are taken from `bar.symbol` + the provided
    /// `tf`. Creates the state lazily if unknown. Notifies observers.
    ///
    /// Returns `Ok(None)` if the bar was rejected (duplicate / out of order).
    pub fn advance(&self, tf: Timeframe, bar: Bar) -> anyhow::Result<Option<AdvanceOutcome>> {
        let arc = self.ensure_symbol(&bar.symbol, tf, None);
        let symbol = bar.symbol.clone();
        let outcome = {
            let mut w = arc.write();
            w.advance(bar)
        };
        if outcome.skipped {
            return Ok(None);
        }
        let observers = {
            let r = self.observers.read();
            r.clone()
        };
        if !observers.is_empty() {
            trace!(
                symbol = %symbol,
                ?tf,
                bar_ts = outcome.ts,
                produced = outcome.new_values.len(),
                observers = observers.len(),
                "fanning out advance to observers",
            );
        }
        for obs in &observers {
            obs.on_advance(&symbol, tf, &outcome);
        }
        Ok(Some(outcome))
    }

    /// Read-only snapshot for handing to an HTTP handler. `None` if the
    /// (symbol, tf) has never been advanced.
    pub fn snapshot(&self, symbol: &str, tf: Timeframe) -> Option<SymbolSnapshot> {
        let arc = self.state(symbol, tf)?;
        let r = arc.read();
        Some(r.snapshot())
    }

    /// Convenience: run `f` under a read guard without cloning.
    /// Useful when callers only need a few fields and want to avoid the
    /// full-snapshot allocation on a hot path.
    pub fn with_state<R>(
        &self,
        symbol: &str,
        tf: Timeframe,
        f: impl FnOnce(&SymbolState) -> R,
    ) -> Option<R> {
        let arc = self.state(symbol, tf)?;
        let r = arc.read();
        Some(f(&r))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn mk_bar(sym: &str, t: i64, c: f64) -> Bar {
        Bar::new(t, sym, c, c, c, c, 1.0)
    }

    #[test]
    fn default_config_has_sane_windows() {
        let cfg = LedgerConfig::default();
        assert_eq!(cfg.default_window_for(Timeframe::M1), 1000);
        assert_eq!(cfg.default_window_for(Timeframe::D1), 252);
    }

    #[test]
    fn required_capacity_scales_with_period() {
        let cfg = LedgerConfig::default();
        // longest_period 200 on D1 (default 252) — 5*200 + 100 = 1100 > 252
        assert_eq!(cfg.required_capacity(Timeframe::D1, 200), 1100);
        // small period on M1 (default 1000) — still defaults
        assert_eq!(cfg.required_capacity(Timeframe::M1, 14), 1000);
    }

    #[test]
    fn ensure_symbol_then_advance() {
        let led = Ledger::new(LedgerConfig::default());
        let arc = led.ensure_symbol("BTCUSDT", Timeframe::M1, None);
        assert!(arc.read().bar_window.is_empty());
        let out = led.advance(Timeframe::M1, mk_bar("BTCUSDT", 60_000, 100.0)).unwrap();
        assert!(out.is_some());
        assert_eq!(led.snapshot("BTCUSDT", Timeframe::M1).unwrap().bars.len(), 1);
    }

    #[test]
    fn pin_rejects_overlong_period() {
        let mut cfg = LedgerConfig::default();
        cfg.max_period = 100;
        let led = Arc::new(Ledger::new(cfg));
        let spec = IndicatorSpec::from_config(json!({"type":"ema","period":200}), None).unwrap();
        let err = led.pin_indicator("BTCUSDT", Timeframe::M1, spec).unwrap_err();
        assert!(err.to_string().contains("max_period"));
    }

    #[test]
    fn pin_enforces_per_symbol_cap() {
        let mut cfg = LedgerConfig::default();
        cfg.max_indicators_per_symbol = 2;
        let led = Arc::new(Ledger::new(cfg));
        led.pin_indicator("BTCUSDT", Timeframe::M1,
            IndicatorSpec::from_config(json!({"type":"ema","period":20}), None).unwrap()).unwrap();
        led.pin_indicator("BTCUSDT", Timeframe::M1,
            IndicatorSpec::from_config(json!({"type":"ema","period":50}), None).unwrap()).unwrap();
        let err = led.pin_indicator("BTCUSDT", Timeframe::M1,
            IndicatorSpec::from_config(json!({"type":"rsi","period":14}), None).unwrap()).unwrap_err();
        assert!(err.to_string().contains("indicator cap"));
    }

    #[test]
    fn pin_is_spec_deduped() {
        let led = Arc::new(Ledger::new(LedgerConfig::default()));
        let a = IndicatorSpec::from_config(json!({"type":"ema","period":20}), None).unwrap();
        let b = IndicatorSpec::from_config(json!({"type":"ema","period":20}), None).unwrap();
        led.pin_indicator("BTCUSDT", Timeframe::M1, a).unwrap();
        led.pin_indicator("BTCUSDT", Timeframe::M1, b).unwrap();
        let snap = led.snapshot("BTCUSDT", Timeframe::M1).unwrap();
        assert_eq!(snap.indicators.len(), 1);
    }

    #[test]
    fn advance_fans_out_to_observers() {
        use std::sync::atomic::{AtomicUsize, Ordering};

        struct Counter(AtomicUsize);
        impl LedgerObserver for Counter {
            fn on_advance(&self, _symbol: &str, _tf: Timeframe, _outcome: &AdvanceOutcome) {
                self.0.fetch_add(1, Ordering::SeqCst);
            }
        }

        let led = Ledger::new(LedgerConfig::default());
        let counter = Arc::new(Counter(AtomicUsize::new(0)));
        led.subscribe(counter.clone() as Arc<dyn LedgerObserver>);

        for i in 0..5 {
            led.advance(Timeframe::M1, mk_bar("BTCUSDT", (i + 1) * 60_000, 100.0)).unwrap();
        }
        assert_eq!(counter.0.load(Ordering::SeqCst), 5);
    }

    #[test]
    fn advance_with_pinned_indicator_produces_snapshot_series() {
        let led = Arc::new(Ledger::new(LedgerConfig::default()));
        let spec = IndicatorSpec::from_config(json!({"type":"ema","period":3}), None).unwrap();
        led.pin_indicator("BTCUSDT", Timeframe::M1, spec.clone()).unwrap();
        for i in 0..10 {
            led.advance(Timeframe::M1, mk_bar("BTCUSDT", (i + 1) * 60_000, 100.0 + i as f64)).unwrap();
        }
        let snap = led.snapshot("BTCUSDT", Timeframe::M1).unwrap();
        let slice = snap.indicators.get(&spec).unwrap();
        assert_eq!(slice.values.len(), 10);
        assert!(slice.ready_since_t.is_some());
        assert!(slice.values.iter().filter(|v| v.is_some()).count() >= 8);
    }

    // --- handle / refcount tests --------------------------------------

    fn spec_ema(period: u32) -> IndicatorSpec {
        IndicatorSpec::from_config(json!({"type":"ema","period":period}), None).unwrap()
    }

    #[test]
    fn acquire_increments_refcount_and_holds_cell() {
        let led = Arc::new(Ledger::new(LedgerConfig::default()));
        let h = led.acquire_indicator("BTCUSDT", Timeframe::M1, spec_ema(5)).unwrap();
        {
            let arc = led.state("BTCUSDT", Timeframe::M1).unwrap();
            let r = arc.read();
            let cell = r.indicators.get(h.spec()).unwrap();
            assert_eq!(cell.refcount, 1);
            assert!(!cell.pinned);
            assert!(cell.pending_drop_at_counter.is_none());
        }
        drop(h);
        // Drop alone does not remove the cell — grace period still active.
        let arc = led.state("BTCUSDT", Timeframe::M1).unwrap();
        let r = arc.read();
        let cell = r.indicators.get(&spec_ema(5)).expect("cell lingers through grace");
        assert_eq!(cell.refcount, 0);
        assert!(cell.pending_drop_at_counter.is_some());
    }

    #[test]
    fn clone_handle_shares_refcount() {
        let led = Arc::new(Ledger::new(LedgerConfig::default()));
        let h1 = led.acquire_indicator("BTCUSDT", Timeframe::M1, spec_ema(5)).unwrap();
        let h2 = h1.clone();
        {
            let r = led.state("BTCUSDT", Timeframe::M1).unwrap().read().indicators[h1.spec()].refcount;
            // Clone of handle must not bump ledger refcount — that's the
            // whole point of the Arc<HandleInner> optimization.
            assert_eq!(r, 1);
        }
        drop(h1);
        // h2 still alive — no grace scheduled.
        {
            let arc = led.state("BTCUSDT", Timeframe::M1).unwrap();
            let r = arc.read();
            let cell = r.indicators.get(h2.spec()).unwrap();
            assert_eq!(cell.refcount, 1);
            assert!(cell.pending_drop_at_counter.is_none());
        }
        // Read guard dropped — safe to drop h2 which takes the write lock.
        drop(h2);
    }

    #[test]
    fn two_acquires_share_one_cell() {
        let led = Arc::new(Ledger::new(LedgerConfig::default()));
        let h1 = led.acquire_indicator("BTCUSDT", Timeframe::M1, spec_ema(5)).unwrap();
        let h2 = led.acquire_indicator("BTCUSDT", Timeframe::M1, spec_ema(5)).unwrap();
        let arc = led.state("BTCUSDT", Timeframe::M1).unwrap();
        {
            let r = arc.read();
            assert_eq!(r.indicators.len(), 1);
            assert_eq!(r.indicators[h1.spec()].refcount, 2);
        }
        drop(h1);
        {
            let r = arc.read();
            assert_eq!(r.indicators[h2.spec()].refcount, 1);
            assert!(r.indicators[h2.spec()].pending_drop_at_counter.is_none());
        }
        drop(h2);
    }

    #[test]
    fn grace_eviction_fires_after_configured_bars() {
        let mut cfg = LedgerConfig::default();
        cfg.drop_grace_bars = 3;
        let led = Arc::new(Ledger::new(cfg));
        let spec = spec_ema(5);
        let h = led.acquire_indicator("BTCUSDT", Timeframe::M1, spec.clone()).unwrap();
        // Warm a bit.
        for i in 0..10 {
            led.advance(Timeframe::M1, mk_bar("BTCUSDT", (i + 1) * 60_000, 100.0)).unwrap();
        }
        drop(h);
        // One advance — cell lingers.
        led.advance(Timeframe::M1, mk_bar("BTCUSDT", 11 * 60_000, 100.0)).unwrap();
        assert!(led.state("BTCUSDT", Timeframe::M1).unwrap().read().indicators.contains_key(&spec));
        // Two more — grace = 3 → evicted on the 3rd post-drop advance.
        led.advance(Timeframe::M1, mk_bar("BTCUSDT", 12 * 60_000, 100.0)).unwrap();
        led.advance(Timeframe::M1, mk_bar("BTCUSDT", 13 * 60_000, 100.0)).unwrap();
        assert!(
            !led.state("BTCUSDT", Timeframe::M1).unwrap().read().indicators.contains_key(&spec),
            "cell should be evicted after grace expires",
        );
    }

    #[test]
    fn reacquire_during_grace_reuses_warmed_cell() {
        let mut cfg = LedgerConfig::default();
        cfg.drop_grace_bars = 5;
        let led = Arc::new(Ledger::new(cfg));
        let spec = spec_ema(3);
        let h = led.acquire_indicator("BTCUSDT", Timeframe::M1, spec.clone()).unwrap();
        for i in 0..10 {
            led.advance(Timeframe::M1, mk_bar("BTCUSDT", (i + 1) * 60_000, 100.0 + i as f64)).unwrap();
        }
        let ready_before = led
            .state("BTCUSDT", Timeframe::M1).unwrap()
            .read().indicators[&spec].ready_since_t;
        assert!(ready_before.is_some());
        drop(h);
        // Advance once inside grace.
        led.advance(Timeframe::M1, mk_bar("BTCUSDT", 11 * 60_000, 110.0)).unwrap();
        // Re-acquire — should reuse the same cell, same ready_since_t.
        let h2 = led.acquire_indicator("BTCUSDT", Timeframe::M1, spec.clone()).unwrap();
        {
            let arc = led.state("BTCUSDT", Timeframe::M1).unwrap();
            let r = arc.read();
            let cell = r.indicators.get(&spec).unwrap();
            assert_eq!(cell.refcount, 1);
            assert!(cell.pending_drop_at_counter.is_none());
            assert_eq!(cell.ready_since_t, ready_before, "warmup must survive grace reuse");
        }
        drop(h2);
    }

    #[test]
    fn pinned_cells_never_get_evicted() {
        let mut cfg = LedgerConfig::default();
        cfg.drop_grace_bars = 1; // aggressive — would evict immediately
        let led = Arc::new(Ledger::new(cfg));
        let spec = spec_ema(5);
        led.pin_indicator("BTCUSDT", Timeframe::M1, spec.clone()).unwrap();
        for i in 0..20 {
            led.advance(Timeframe::M1, mk_bar("BTCUSDT", (i + 1) * 60_000, 100.0)).unwrap();
        }
        let arc = led.state("BTCUSDT", Timeframe::M1).unwrap();
        let r = arc.read();
        let cell = r.indicators.get(&spec).expect("pinned cell must persist");
        assert!(cell.pinned);
        assert!(cell.pending_drop_at_counter.is_none());
    }

    #[test]
    fn acquire_indicators_returns_handles_in_order() {
        let led = Arc::new(Ledger::new(LedgerConfig::default()));
        let specs = vec![spec_ema(5), spec_ema(20), spec_ema(50)];
        let handles = led
            .acquire_indicators("BTCUSDT", Timeframe::M1, specs.clone())
            .unwrap();
        assert_eq!(handles.len(), 3);
        for (h, s) in handles.iter().zip(specs.iter()) {
            assert_eq!(h.spec(), s);
        }
        let r = led.state("BTCUSDT", Timeframe::M1).unwrap().read().indicators.len();
        assert_eq!(r, 3);
    }
}
