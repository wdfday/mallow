//! Internal multi-timeframe resampler.
//!
//! Bridges the gap between the WebSocket feed (which typically publishes a
//! single base TF — M1 — per symbol) and hands that need higher-TF bars.
//!
//! ```text
//!   ws → ledger.advance(M1, bar)
//!                         │
//!                         ▼
//!                ResampleManager (LedgerObserver)
//!                         │
//!                  for each (symbol, M1→target_tf) entry:
//!                    feed bar into StandaloneAggregator
//!                    if aggregator emits HTF bar:
//!                      ledger.advance(target_tf, htf_bar)
//!                              │
//!                              ▼ (recursive — observers fan out again)
//!                       observers(target_tf, ...)
//! ```
//!
//! # Subscription model
//!
//! - [`ResampleManager::ensure`] is idempotent and refcounted — registering
//!   the same `(symbol, source_tf, target_tf)` twice creates one aggregator
//!   and bumps the refcount.
//! - [`ResampleManager::release`] decrements; when the count hits zero the
//!   aggregator is dropped (no further bars produced for that target).
//! - Multiple distinct `target_tf`s for one `(symbol, source_tf)` share zero
//!   state — each has its own aggregator.
//!
//! # Cycle safety
//!
//! `ResampleManager` holds a `Weak<Ledger>` so the (manager → ledger)
//! direction never extends ledger lifetime. The ledger holds
//! `Arc<dyn LedgerObserver>` for the manager — straightforward owner.
//! No reference cycle.

use std::sync::{Arc, Weak};

use alm_core::Timeframe;
use alm_data::aggregator::StandaloneAggregator;
use alm_ledger::{AdvanceOutcome, Ledger, LedgerObserver};
use dashmap::DashMap;
use parking_lot::Mutex;
use tracing::{debug, trace, warn};

type Key = (String, Timeframe, Timeframe);

struct Entry {
    aggregator: StandaloneAggregator,
    refcount: usize,
}

/// Drives `(source_tf → target_tf)` resampling for every registered key.
pub struct ResampleManager {
    ledger: Weak<Ledger>,
    /// `(symbol, source_tf, target_tf) → entry`. The outer `Mutex` on each
    /// entry serialises the aggregator update so observers can run on
    /// multiple symbols concurrently without false sharing.
    subs: DashMap<Key, Mutex<Entry>>,
}

impl ResampleManager {
    pub fn new(ledger: Weak<Ledger>) -> Arc<Self> {
        Arc::new(Self {
            ledger,
            subs: DashMap::new(),
        })
    }

    /// Register intent to resample `source_tf → target_tf` for `symbol`.
    /// Idempotent and refcounted.
    pub fn ensure(&self, symbol: &str, source_tf: Timeframe, target_tf: Timeframe) {
        if source_tf == target_tf {
            return;
        }
        if source_tf.duration_ms() >= target_tf.duration_ms() {
            warn!(
                %symbol, ?source_tf, ?target_tf,
                "ensure_resample: target_tf must be larger than source_tf — skipping"
            );
            return;
        }
        let key = (symbol.to_string(), source_tf, target_tf);
        self.subs
            .entry(key)
            .and_modify(|m| m.lock().refcount += 1)
            .or_insert_with(|| {
                debug!(%symbol, ?source_tf, ?target_tf, "resample subscription created");
                Mutex::new(Entry {
                    aggregator: StandaloneAggregator::new(target_tf),
                    refcount: 1,
                })
            });
    }

    /// Decrement refcount; drop the aggregator if it reaches zero.
    pub fn release(&self, symbol: &str, source_tf: Timeframe, target_tf: Timeframe) {
        let key = (symbol.to_string(), source_tf, target_tf);
        let drop_now = if let Some(entry) = self.subs.get(&key) {
            let mut g = entry.lock();
            if g.refcount > 0 {
                g.refcount -= 1;
            }
            g.refcount == 0
        } else {
            false
        };
        if drop_now {
            self.subs.remove(&key);
            debug!(%symbol, ?source_tf, ?target_tf, "resample subscription removed");
        }
    }

    /// Number of active subscriptions (test helper).
    #[cfg(test)]
    pub fn len(&self) -> usize {
        self.subs.len()
    }
}

impl LedgerObserver for ResampleManager {
    fn on_advance(&self, symbol: &str, tf: Timeframe, outcome: &AdvanceOutcome) {
        let Some(ledger) = self.ledger.upgrade() else {
            return;
        };

        // Pull the just-advanced bar from the ledger. It's the back of the
        // window because advance() pushed it before notifying us.
        let bar = match ledger.with_state(symbol, tf, |s| s.bar_window.back().cloned()) {
            Some(Some(b)) => b,
            _ => return,
        };
        // Defensive: timestamps must match. If they don't, another advance
        // already pushed before we ran — skip rather than emit a wrong bar.
        if bar.timestamp != outcome.ts {
            trace!(
                %symbol, ?tf,
                bar_ts = bar.timestamp, outcome_ts = outcome.ts,
                "resample: bar/outcome ts mismatch, skipping"
            );
            return;
        }

        // Collect emissions outside the DashMap lock to avoid holding it
        // across `ledger.advance` (which itself takes the observers lock).
        let mut emissions: Vec<(Timeframe, alm_core::Bar)> = Vec::new();
        for entry in self.subs.iter() {
            let (s, src, tgt) = entry.key();
            if s != symbol || *src != tf {
                continue;
            }
            let mut g = entry.value().lock();
            if let Some(htf_bar) = g.aggregator.update(&bar) {
                emissions.push((*tgt, htf_bar));
            }
        }

        for (target_tf, htf_bar) in emissions {
            trace!(
                %symbol, ?tf, ?target_tf,
                bar_ts = htf_bar.timestamp,
                "resample: emitting HTF bar"
            );
            if let Err(e) = ledger.advance(target_tf, htf_bar) {
                warn!(%symbol, ?target_tf, err = %e, "resample: ledger.advance failed");
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::Bar;
    use alm_ledger::{Ledger, LedgerConfig};

    fn bar(ts: i64, sym: &str, c: f64) -> Bar {
        Bar::new(ts, sym, c, c, c, c, 1.0)
    }

    #[test]
    fn ensure_refcounts_idempotent() {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let mgr = ResampleManager::new(Arc::downgrade(&ledger));
        mgr.ensure("BTCUSDT", Timeframe::M1, Timeframe::H1);
        mgr.ensure("BTCUSDT", Timeframe::M1, Timeframe::H1);
        assert_eq!(mgr.len(), 1);
        mgr.release("BTCUSDT", Timeframe::M1, Timeframe::H1);
        assert_eq!(mgr.len(), 1, "still one refcount left");
        mgr.release("BTCUSDT", Timeframe::M1, Timeframe::H1);
        assert_eq!(mgr.len(), 0, "dropped at refcount zero");
    }

    #[test]
    fn rejects_invalid_target() {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let mgr = ResampleManager::new(Arc::downgrade(&ledger));
        // target_tf < source_tf — rejected
        mgr.ensure("BTCUSDT", Timeframe::H1, Timeframe::M1);
        assert_eq!(mgr.len(), 0);
        // source_tf == target_tf — no-op
        mgr.ensure("BTCUSDT", Timeframe::M1, Timeframe::M1);
        assert_eq!(mgr.len(), 0);
    }

    /// When an external feed already advanced the target TF, our synthetic
    /// emission must be silently dropped — `Ledger::advance` returns
    /// `outcome.skipped = true` for duplicate timestamps and short-circuits
    /// the observer fan-out. The H1 window must contain only ONE bar (the
    /// external one), with the external close value (not the resample close).
    #[test]
    fn external_htf_advance_takes_precedence_over_resample() {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let mgr = ResampleManager::new(Arc::downgrade(&ledger));
        ledger.subscribe(mgr.clone() as Arc<dyn LedgerObserver>);
        mgr.ensure("BTCUSDT", Timeframe::M1, Timeframe::H1);

        // External H1 bar arrives FIRST for bucket starting at t=0, close=999.0.
        ledger.advance(Timeframe::H1, bar(0, "BTCUSDT", 999.0)).unwrap();

        // Now 61 M1 bars: 0..59 fill bucket 0, 60th opens bucket 1 and would
        // normally trigger the resampler to emit an H1 for bucket 0 (close
        // equal to the LAST M1 close = 100.0+59 = 159.0). Ledger must reject
        // that emission as duplicate ts=0.
        for i in 0..61_i64 {
            ledger.advance(Timeframe::M1, bar(i * 60_000, "BTCUSDT", 100.0 + i as f64)).unwrap();
        }

        // Inspect the H1 window: exactly one bar, with the external close.
        let h1_bars = ledger
            .with_state("BTCUSDT", Timeframe::H1, |s| s.bar_window.iter().cloned().collect::<Vec<_>>())
            .unwrap_or_default();
        assert_eq!(h1_bars.len(), 1, "duplicate H1 emission must be skipped");
        assert!(
            (h1_bars[0].close - 999.0).abs() < 1e-9,
            "external H1 close (999.0) must win over resampled close, got {}",
            h1_bars[0].close
        );
    }

    #[test]
    fn m1_advances_emit_h1_bar_at_bucket_close() {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let mgr = ResampleManager::new(Arc::downgrade(&ledger));
        ledger.subscribe(mgr.clone() as Arc<dyn LedgerObserver>);
        mgr.ensure("BTCUSDT", Timeframe::M1, Timeframe::H1);

        // 60 M1 bars covering one H1 bucket (timestamps 0..59 minutes).
        // The 61st bar (at hour 1) closes the first H1 bucket.
        for i in 0..60_i64 {
            ledger.advance(Timeframe::M1, bar(i * 60_000, "BTCUSDT", 100.0 + i as f64)).unwrap();
        }
        // No H1 bar emitted yet — the aggregator emits on the FIRST bar of
        // the NEXT bucket.
        let h1_window_pre = ledger
            .with_state("BTCUSDT", Timeframe::H1, |s| s.bar_window.len())
            .unwrap_or(0);
        assert_eq!(h1_window_pre, 0);

        // Push the first bar of the next H1 bucket.
        ledger.advance(Timeframe::M1, bar(60 * 60_000, "BTCUSDT", 200.0)).unwrap();
        let h1_window_post = ledger
            .with_state("BTCUSDT", Timeframe::H1, |s| s.bar_window.len())
            .unwrap_or(0);
        assert_eq!(h1_window_post, 1, "one H1 bar should have been emitted");
    }
}
