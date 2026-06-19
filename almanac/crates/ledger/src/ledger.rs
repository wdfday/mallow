//! `Ledger` — live market-state container.
//!
//! Concurrency model:
//! - `DashMap<(Symbol, Timeframe), Arc<RwLock<SymbolState>>>` — sharded,
//!   so different symbols never contend.
//! - Writers (bar ingestion) take the per-state `RwLock` write guard for
//!   the short window needed to push the bar.
//! - Readers (registry evaluate, HTTP endpoints) take the read guard via
//!   `with_state()`.

use std::collections::HashMap;
use std::sync::Arc;

use alm_core::{Bar, Timeframe};
use dashmap::DashMap;
use metrics::counter;
use parking_lot::RwLock;
use serde::{Deserialize, Serialize};
use tracing::{debug, error, trace};

use crate::state::{AdvanceOutcome, SymbolState};

/// `(Symbol, Timeframe)` map key.
pub type SymbolKey = (String, Timeframe);

/// Per-timeframe window configuration.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LedgerConfig {
    /// Default window size per timeframe. Falls back to `fallback_window`
    /// when a TF is not listed.
    pub default_window: HashMap<Timeframe, usize>,
    /// Used when `default_window` has no entry for the requested TF.
    pub fallback_window: usize,
}

impl Default for LedgerConfig {
    fn default() -> Self {
        // Per-TF windows sized so the live ledger always overlaps with the
        // most-recent parquet bar, even when parquet generation lags up to
        // 24h. The seam ledger↔DuckDB falls inside the parquet horizon so
        // pagination never produces a "phantom gap" at the boundary.
        //
        // Math: M1 needs ≥ 24h × 60 = 1440 bars just to cover one parquet
        // write cycle; we use 2000 to absorb extra lag and weekend stalls.
        let mut default_window = HashMap::new();
        default_window.insert(Timeframe::M1,  2000); // ~33h
        default_window.insert(Timeframe::M3,  1500); // ~75h
        default_window.insert(Timeframe::M5,  1500); // ~125h
        default_window.insert(Timeframe::M15, 1000); // ~10d
        default_window.insert(Timeframe::M30, 1000); // ~20d
        Self { default_window, fallback_window: 1000 }
    }
}

impl LedgerConfig {
    pub fn default_window_for(&self, tf: Timeframe) -> usize {
        *self.default_window.get(&tf).unwrap_or(&self.fallback_window)
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

    pub fn config(&self) -> &LedgerConfig {
        &self.cfg
    }

    /// Register an observer. Cheap — observers are rarely added/removed.
    pub fn subscribe(&self, observer: Arc<dyn LedgerObserver>) {
        self.observers.write().push(observer);
    }

    /// Ensure a `SymbolState` exists for this `(symbol, tf)` with at least
    /// the given capacity (grows but never shrinks). Returns the Arc for
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

    /// All `(symbol, tf)` pairs currently tracked.
    pub fn keys(&self) -> Vec<SymbolKey> {
        self.states.iter().map(|r| r.key().clone()).collect()
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
                observers = observers.len(),
                "fanning out advance to observers",
            );
        }
        for obs in &observers {
            let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                obs.on_advance(&symbol, tf, &outcome);
            }));
            if let Err(payload) = result {
                let msg = payload.downcast_ref::<&str>().copied()
                    .or_else(|| payload.downcast_ref::<String>().map(String::as_str))
                    .unwrap_or("<non-string panic>");
                error!(
                    symbol = %symbol, ?tf, bar_ts = outcome.ts, panic_msg = %msg,
                    "ledger observer panicked — skipping to next observer"
                );
                counter!("herald_ledger_observer_panics_total").increment(1);
            }
        }
        Ok(Some(outcome))
    }

    /// Update the forming bar for `(symbol, tf)` without confirming it.
    ///
    /// Called after every base-TF bar advance to keep `SymbolState::live_bar`
    /// current for all actively-tracked HTF slices. Does **not** notify
    /// observers — `live_bar` is a read-only peek for HTTP / strategy use.
    pub fn advance_live(&self, tf: Timeframe, bar: Bar) {
        let arc = self.ensure_symbol(&bar.symbol, tf, None);
        arc.write().advance_live(bar);
    }

    /// Convenience: run `f` under a read guard without cloning.
    /// Useful when callers only need a few fields (e.g. `last_ts`, `bar_window.back()`).
    pub fn with_state<R>(
        &self,
        symbol: &str,
        tf: Timeframe,
        f: impl FnOnce(&SymbolState) -> R,
    ) -> Option<R> {
        let arc = self.states.get(&(symbol.to_string(), tf))?;
        let r = arc.read();
        Some(f(&r))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn mk_bar(sym: &str, t: i64, c: f64) -> Bar {
        Bar::new(t, sym, c, c, c, c, 1.0)
    }

    #[test]
    fn default_config_has_sane_windows() {
        let cfg = LedgerConfig::default();
        assert_eq!(cfg.default_window_for(Timeframe::M1), 2000);
        assert_eq!(cfg.default_window_for(Timeframe::M3), 1500);
        assert_eq!(cfg.default_window_for(Timeframe::M5), 1500);
        assert_eq!(cfg.default_window_for(Timeframe::M15), 1000);
        assert_eq!(cfg.default_window_for(Timeframe::H12), 1000);
        assert_eq!(cfg.default_window_for(Timeframe::D1), 1000);
    }

    #[test]
    fn ensure_symbol_then_advance() {
        let led = Ledger::new(LedgerConfig::default());
        let arc = led.ensure_symbol("BTCUSDT", Timeframe::M1, None);
        assert!(arc.read().bar_window.is_empty());
        let out = led.advance(Timeframe::M1, mk_bar("BTCUSDT", 60_000, 100.0)).unwrap();
        assert!(out.is_some());
        assert_eq!(led.with_state("BTCUSDT", Timeframe::M1, |s| s.bar_window.len()), Some(1));
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
    fn advance_skipped_bar_does_not_fan_out() {
        use std::sync::atomic::{AtomicUsize, Ordering};

        struct Counter(AtomicUsize);
        impl LedgerObserver for Counter {
            fn on_advance(&self, _: &str, _: Timeframe, _: &AdvanceOutcome) {
                self.0.fetch_add(1, Ordering::SeqCst);
            }
        }

        let led = Ledger::new(LedgerConfig::default());
        let counter = Arc::new(Counter(AtomicUsize::new(0)));
        led.subscribe(counter.clone() as Arc<dyn LedgerObserver>);

        led.advance(Timeframe::M1, mk_bar("BTCUSDT", 60_000, 100.0)).unwrap();
        led.advance(Timeframe::M1, mk_bar("BTCUSDT", 30_000, 99.0)).unwrap(); // skipped
        assert_eq!(counter.0.load(Ordering::SeqCst), 1);
    }

    #[test]
    fn keys_returns_all_tracked_pairs() {
        let led = Ledger::new(LedgerConfig::default());
        led.advance(Timeframe::M1, mk_bar("BTCUSDT", 60_000, 1.0)).unwrap();
        led.advance(Timeframe::M1, mk_bar("ETHUSDT", 60_000, 1.0)).unwrap();
        led.advance(Timeframe::H1, mk_bar("BTCUSDT", 3_600_000, 1.0)).unwrap();
        assert_eq!(led.keys().len(), 3);
    }
}
