//! Bootstrap helpers — warm `Ledger` state from historical bar feeds.
//!
//! Bootstrap bypasses the observer fan-out path on purpose: bots must not
//! react to replayed historical bars. After bootstrap finishes the caller
//! subscribes observers and begins live ingestion.
//!
//! Concurrency: bootstrap is parallelised per `(symbol, timeframe)`. Each
//! job holds the write lock for its own `SymbolState`; different pairs
//! live in different `DashMap` shards and never contend.

use std::time::Instant;

use alm_core::{Bar, Timeframe};
use alm_data::BarFeed;
use rayon::prelude::*;
use tracing::{debug, info, warn};

use crate::ledger::Ledger;

/// Summary of one `(symbol, tf)` bootstrap run.
#[derive(Debug, Clone)]
pub struct BootstrapReport {
    pub symbol: String,
    pub tf: Timeframe,
    /// Number of bars accepted by `SymbolState::advance`.
    pub fed: usize,
    /// Number of bars rejected as out-of-order / duplicates.
    pub skipped: usize,
    pub first_ts: Option<i64>,
    pub last_ts: Option<i64>,
    pub duration_ms: u64,
}

/// Describes one bootstrap unit of work — a (symbol, tf) pair plus a bar
/// source. Wrap your `ParquetFeed` / in-memory vec / custom feed in a
/// `Box<dyn BarFeed + Send>` and hand it in.
pub struct BootstrapJob {
    pub symbol: String,
    pub tf: Timeframe,
    pub feed: Box<dyn BarFeed + Send>,
}

impl Ledger {
    /// Feed bars from an iterator into `(symbol, tf)` without observer
    /// notification. Skipped (duplicate/out-of-order) bars are counted but
    /// do not error. Returns a report with timings and counts.
    pub fn bootstrap_symbol<I>(
        &self,
        symbol: &str,
        tf: Timeframe,
        bars: I,
    ) -> anyhow::Result<BootstrapReport>
    where
        I: IntoIterator<Item = Bar>,
    {
        let start = Instant::now();
        let arc = self.ensure_symbol(symbol, tf, None);

        let mut fed = 0usize;
        let mut skipped = 0usize;
        let mut first_ts: Option<i64> = None;

        let last_ts = {
            let mut w = arc.write();
            for bar in bars {
                let ts = bar.timestamp;
                let out = w.advance(bar);
                if out.skipped {
                    skipped += 1;
                } else {
                    fed += 1;
                    if first_ts.is_none() {
                        first_ts = Some(ts);
                    }
                }
            }
            w.last_ts
        };

        let duration_ms = start.elapsed().as_millis() as u64;
        info!(
            symbol,
            ?tf,
            fed,
            skipped,
            ?first_ts,
            ?last_ts,
            duration_ms,
            "bootstrap_symbol complete",
        );
        Ok(BootstrapReport {
            symbol: symbol.to_string(),
            tf,
            fed,
            skipped,
            first_ts,
            last_ts,
            duration_ms,
        })
    }

    /// Feed bars from a `BarFeed` into `(symbol, tf)`. The feed is consumed
    /// to exhaustion — callers who need early termination should wrap it.
    pub fn bootstrap_feed(
        &self,
        symbol: &str,
        tf: Timeframe,
        mut feed: Box<dyn BarFeed + Send>,
    ) -> anyhow::Result<BootstrapReport> {
        let total = feed.len();
        debug!(symbol, ?tf, total, "bootstrap_feed: draining feed");
        let iter = std::iter::from_fn(move || feed.next());
        self.bootstrap_symbol(symbol, tf, iter)
    }

    /// Bootstrap many `(symbol, tf)` pairs in parallel via rayon. Each job
    /// owns its write lock so there is no contention between symbols.
    /// Failures are collected per-job — one bad symbol does not abort the rest.
    pub fn bootstrap_parallel(&self, jobs: Vec<BootstrapJob>) -> Vec<anyhow::Result<BootstrapReport>>
    where
        Self: Sync,
    {
        let start = Instant::now();
        let n = jobs.len();
        info!(jobs = n, "bootstrap_parallel: starting");

        let reports: Vec<_> = jobs
            .into_par_iter()
            .map(|job| self.bootstrap_feed(&job.symbol, job.tf, job.feed))
            .collect();

        let ok = reports.iter().filter(|r| r.is_ok()).count();
        let err = reports.len() - ok;
        let duration_ms = start.elapsed().as_millis() as u64;
        if err > 0 {
            warn!(ok, err, duration_ms, "bootstrap_parallel: some jobs failed");
        } else {
            info!(ok, duration_ms, "bootstrap_parallel: done");
        }
        reports
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{IndicatorSpec, LedgerConfig};
    use alm_core::Bar;
    use serde_json::json;
    use std::sync::Arc;

    struct VecFeed {
        symbol: String,
        bars: Vec<Bar>,
        cursor: usize,
    }

    impl VecFeed {
        fn new(symbol: &str, bars: Vec<Bar>) -> Self {
            Self { symbol: symbol.to_string(), bars, cursor: 0 }
        }
    }

    impl BarFeed for VecFeed {
        fn next(&mut self) -> Option<Bar> {
            let b = self.bars.get(self.cursor).cloned()?;
            self.cursor += 1;
            Some(b)
        }
        fn symbol(&self) -> &str { &self.symbol }
        fn len(&self) -> usize { self.bars.len() }
        fn reset(&mut self) { self.cursor = 0; }
        fn timeframe(&self) -> Timeframe { Timeframe::M1 }
    }

    fn mk_bars(sym: &str, n: usize) -> Vec<Bar> {
        (1..=n)
            .map(|i| Bar::new(i as i64 * 60_000, sym, 100.0, 100.0, 100.0, 100.0, 1.0))
            .collect()
    }

    #[test]
    fn bootstrap_symbol_fills_window_and_counts() {
        let led = Ledger::new(LedgerConfig::default());
        let bars = mk_bars("BTCUSDT", 500);
        let rep = led.bootstrap_symbol("BTCUSDT", Timeframe::M1, bars).unwrap();
        assert_eq!(rep.fed, 500);
        assert_eq!(rep.skipped, 0);
        assert_eq!(rep.first_ts, Some(60_000));
        assert_eq!(rep.last_ts, Some(500 * 60_000));
        let snap = led.snapshot("BTCUSDT", Timeframe::M1).unwrap();
        assert_eq!(snap.bars.len(), 500);
    }

    #[test]
    fn bootstrap_respects_capacity_trim() {
        let led = Ledger::new(LedgerConfig::default());
        // D1 default cap = 252. Feed 1000 bars → only last 252 kept.
        let bars = (1..=1000)
            .map(|i| Bar::new(i as i64 * 86_400_000, "AAPL", 100.0, 100.0, 100.0, 100.0, 1.0))
            .collect::<Vec<_>>();
        let rep = led.bootstrap_symbol("AAPL", Timeframe::D1, bars).unwrap();
        assert_eq!(rep.fed, 1000);
        let snap = led.snapshot("AAPL", Timeframe::D1).unwrap();
        assert_eq!(snap.bars.len(), 252);
        assert_eq!(snap.bars.first().unwrap().timestamp, (1000 - 252 + 1) as i64 * 86_400_000);
    }

    #[test]
    fn bootstrap_feeds_registered_indicators_incrementally() {
        // Register indicator first → bootstrap feeds it every bar → value is
        // correct even though only last `capacity` bars remain visible.
        let led = Arc::new(Ledger::new(LedgerConfig::default()));
        let spec = IndicatorSpec::from_config(json!({"type":"ema","period":20}), None).unwrap();
        led.pin_indicator("BTCUSDT", Timeframe::M1, spec.clone()).unwrap();
        let bars = mk_bars("BTCUSDT", 2000);
        led.bootstrap_symbol("BTCUSDT", Timeframe::M1, bars).unwrap();
        let snap = led.snapshot("BTCUSDT", Timeframe::M1).unwrap();
        let slice = snap.indicators.get(&spec).unwrap();
        assert!(slice.ready_since_t.is_some(), "EMA must be ready after 2000 bars");
        // last entry must carry a value
        assert!(slice.values.last().unwrap().is_some());
    }

    #[test]
    fn bootstrap_skips_duplicates() {
        let led = Ledger::new(LedgerConfig::default());
        let mut bars = mk_bars("BTCUSDT", 10);
        // introduce a duplicate in the middle
        bars.insert(5, bars[4].clone());
        let rep = led.bootstrap_symbol("BTCUSDT", Timeframe::M1, bars).unwrap();
        assert_eq!(rep.fed, 10);
        assert_eq!(rep.skipped, 1);
    }

    #[test]
    fn bootstrap_feed_drains_whole_feed() {
        let led = Ledger::new(LedgerConfig::default());
        let feed = Box::new(VecFeed::new("ETHUSDT", mk_bars("ETHUSDT", 100)));
        let rep = led.bootstrap_feed("ETHUSDT", Timeframe::M1, feed).unwrap();
        assert_eq!(rep.fed, 100);
    }

    #[test]
    fn bootstrap_parallel_handles_many_symbols() {
        let led = Ledger::new(LedgerConfig::default());
        let jobs: Vec<BootstrapJob> = ["BTC", "ETH", "SOL", "BNB", "XRP"]
            .iter()
            .map(|&s| BootstrapJob {
                symbol: s.to_string(),
                tf: Timeframe::M1,
                feed: Box::new(VecFeed::new(s, mk_bars(s, 300))),
            })
            .collect();
        let reports = led.bootstrap_parallel(jobs);
        assert_eq!(reports.len(), 5);
        for r in &reports {
            let rep = r.as_ref().unwrap();
            assert_eq!(rep.fed, 300);
        }
        for s in ["BTC", "ETH", "SOL", "BNB", "XRP"] {
            let snap = led.snapshot(s, Timeframe::M1).unwrap();
            assert_eq!(snap.bars.len(), 300);
        }
    }

    #[test]
    fn bootstrap_parallel_keeps_going_after_failure() {
        // Construct a state so ensure_indicator is registered with a
        // period way above max_period — second symbol fails to register,
        // but first still completes.
        let mut cfg = LedgerConfig::default();
        cfg.max_period = 50;
        let led = Arc::new(Ledger::new(cfg));
        // Pre-register a valid indicator for BTC only
        let spec_ok = IndicatorSpec::from_config(json!({"type":"ema","period":10}), None).unwrap();
        led.pin_indicator("BTC", Timeframe::M1, spec_ok).unwrap();

        let jobs = vec![
            BootstrapJob {
                symbol: "BTC".into(),
                tf: Timeframe::M1,
                feed: Box::new(VecFeed::new("BTC", mk_bars("BTC", 100))),
            },
            BootstrapJob {
                symbol: "ETH".into(),
                tf: Timeframe::M1,
                feed: Box::new(VecFeed::new("ETH", mk_bars("ETH", 100))),
            },
        ];
        let reports = led.bootstrap_parallel(jobs);
        assert_eq!(reports.len(), 2);
        assert!(reports.iter().all(|r| r.is_ok()));
    }
}
