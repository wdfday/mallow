//! Bootstrap helpers — warm `Ledger` state from historical bar feeds.
//!
//! Bootstrap bypasses the observer fan-out path on purpose: bots must not
//! react to replayed historical bars. After bootstrap finishes the caller
//! subscribes observers and begins live ingestion.

use std::time::Instant;

use alm_core::{Bar, Timeframe};
use tracing::info;

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
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::LedgerConfig;
    use alm_core::Bar;

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
        assert_eq!(
            led.with_state("BTCUSDT", Timeframe::M1, |s| s.bar_window.len()),
            Some(500)
        );
    }

    #[test]
    fn bootstrap_respects_capacity_trim() {
        let led = Ledger::new(LedgerConfig::default());
        let bars = (1..=2000)
            .map(|i| Bar::new(i as i64 * 86_400_000, "AAPL", 100.0, 100.0, 100.0, 100.0, 1.0))
            .collect::<Vec<_>>();
        let rep = led.bootstrap_symbol("AAPL", Timeframe::D1, bars).unwrap();
        assert_eq!(rep.fed, 2000);
        let window_len =
            led.with_state("AAPL", Timeframe::D1, |s| s.bar_window.len()).unwrap();
        assert_eq!(window_len, 1000);
    }

    #[test]
    fn bootstrap_skips_duplicates() {
        let led = Ledger::new(LedgerConfig::default());
        let mut bars = mk_bars("BTCUSDT", 10);
        bars.insert(5, bars[4].clone());
        let rep = led.bootstrap_symbol("BTCUSDT", Timeframe::M1, bars).unwrap();
        assert_eq!(rep.fed, 10);
        assert_eq!(rep.skipped, 1);
    }
}
