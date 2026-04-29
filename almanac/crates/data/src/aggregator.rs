//! Bar aggregation — resample a base feed into a higher timeframe.
//!
//! Uses calendar-aligned bucketing: an H1 bar always starts at :00,
//! an H4 bar at 00:00, 04:00, 08:00, … UTC.  This mirrors how exchanges
//! publish candles and avoids artifacts at session boundaries.
//!
//! # Example
//! ```rust,ignore
//! // Strategy that needs both M5 entry-timing and H1 trend context
//! let base_feed  = ParquetFeed::load(path, "BTCUSDT")?;
//! let h1_feed    = BarAggregator::new(base_feed, Timeframe::H1);
//!
//! // Or keep the base feed and hold an aggregator inside the strategy:
//! // let mut h1_agg = BarAggregator::standalone(Timeframe::H1);
//! // within on_bar(): if let Some(h1) = h1_agg.update(&bar) { ... }
//! ```

use std::collections::VecDeque;

use alm_core::{Bar, Timeframe};

use crate::feed::BarFeed;

// ── Standalone aggregator (used inside strategies) ────────────────────────────

/// Stateful aggregator that accumulates base bars into higher-TF bars.
///
/// Intended to be held **inside a strategy** so multiple TFs can be tracked
/// without changing the `BarFeed` or `Engine` API.
///
/// ```rust,ignore
/// let mut h4 = StandaloneAggregator::new(Timeframe::H4);
/// // inside Strategy::on_bar():
/// if let Some(h4_bar) = h4.update(&bar) {
///     // process completed H4 bar
/// }
/// ```
pub struct StandaloneAggregator {
    target_tf: Timeframe,
    pending:   Option<PartialBar>,
}

impl StandaloneAggregator {
    pub fn new(target_tf: Timeframe) -> Self {
        Self { target_tf, pending: None }
    }

    /// Feed a base bar.  Returns `Some(completed_bar)` when the current
    /// higher-TF period rolls over; `None` otherwise.
    pub fn update(&mut self, bar: &Bar) -> Option<Bar> {
        let period = period_start(bar.timestamp, self.target_tf.duration_ms());
        let completed = match &self.pending {
            Some(p) if p.period_start != period => self.flush(),
            _ => None,
        };
        self.update_pending(bar, period);
        completed
    }

    /// Flush the in-progress bar (call at feed exhaustion if needed).
    pub fn flush(&mut self) -> Option<Bar> {
        self.pending.take().map(PartialBar::into_bar)
    }

    fn update_pending(&mut self, bar: &Bar, period: i64) {
        match &mut self.pending {
            None => {
                self.pending = Some(PartialBar::open(bar, period));
            }
            Some(p) => p.extend(bar),
        }
    }
}

// ── Feed-wrapping aggregator ──────────────────────────────────────────────────

/// Wraps a `BarFeed` and transparently resamples it to `target_tf`.
///
/// The `len()` reported is the *base* feed's length (approximate bars remaining).
pub struct BarAggregator<F: BarFeed> {
    inner:     F,
    target_tf: Timeframe,
    agg:       StandaloneAggregator,
    /// Completed bars waiting to be consumed by `next()`.
    ready:     VecDeque<Bar>,
}

impl<F: BarFeed> BarAggregator<F> {
    pub fn new(inner: F, target_tf: Timeframe) -> Self {
        Self {
            agg: StandaloneAggregator::new(target_tf),
            inner,
            target_tf,
            ready: VecDeque::new(),
        }
    }

    pub fn inner(&self)     -> &F { &self.inner }
    pub fn into_inner(self) -> F  { self.inner }
}

impl<F: BarFeed> BarFeed for BarAggregator<F> {
    fn next(&mut self) -> Option<Bar> {
        // Drain already-completed bars first.
        if let Some(bar) = self.ready.pop_front() {
            return Some(bar);
        }

        // Pull from the inner feed until we get a completed higher-TF bar.
        while let Some(base) = self.inner.next() {
            if let Some(completed) = self.agg.update(&base) {
                return Some(completed);
            }
        }

        // Inner feed exhausted — flush the last partial bar.
        self.agg.flush()
    }

    fn symbol(&self)    -> &str      { self.inner.symbol() }
    /// Approximate: returns base-feed bar count (not aggregated bar count).
    fn len(&self)       -> usize     { self.inner.len() }
    fn timeframe(&self) -> Timeframe { self.target_tf }

    fn reset(&mut self) {
        self.inner.reset();
        self.agg = StandaloneAggregator::new(self.target_tf);
        self.ready.clear();
    }
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Round `ts` down to the start of its `period_ms` bucket (UTC-aligned).
#[inline]
fn period_start(ts: i64, period_ms: i64) -> i64 {
    (ts / period_ms) * period_ms
}

struct PartialBar {
    period_start: i64,
    timestamp:    i64,
    symbol:       String,
    open:         f64,
    high:         f64,
    low:          f64,
    close:        f64,
    volume:       f64,
}

impl PartialBar {
    fn open(bar: &Bar, period: i64) -> Self {
        Self {
            period_start: period,
            timestamp:    bar.timestamp,
            symbol:       bar.symbol.clone(),
            open:         bar.open,
            high:         bar.high,
            low:          bar.low,
            close:        bar.close,
            volume:       bar.volume,
        }
    }

    fn extend(&mut self, bar: &Bar) {
        self.high   = self.high.max(bar.high);
        self.low    = self.low.min(bar.low);
        self.close  = bar.close;
        self.volume += bar.volume;
    }

    fn into_bar(self) -> Bar {
        Bar::new(
            self.timestamp,
            &self.symbol,
            self.open,
            self.high,
            self.low,
            self.close,
            self.volume,
        )
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::memory::BarVecFeed;

    fn make_bar(ts: i64, o: f64, h: f64, l: f64, c: f64, v: f64) -> Bar {
        Bar::new(ts, "SYM", o, h, l, c, v)
    }

    fn m1_feed(minutes: usize) -> BarVecFeed {
        let bars: Vec<Bar> = (0..minutes as i64)
            .map(|i| make_bar(i * 60_000, 100.0, 101.0, 99.0, 100.5, 10.0))
            .collect();
        BarVecFeed::new(bars, "SYM".into())
    }

    #[test]
    fn aggregates_5_m1_into_m5() {
        // 10 M1 bars → 2 M5 bars
        let feed = m1_feed(10);
        let mut agg = BarAggregator::new(feed, Timeframe::M5);
        let bars: Vec<Bar> = std::iter::from_fn(|| agg.next()).collect();
        assert_eq!(bars.len(), 2);
        // first M5: ts = 0 (start of first M5 period)
        assert_eq!(bars[0].timestamp, 0);
        // volume summed over 5 M1 bars
        assert!((bars[0].volume - 50.0).abs() < 1e-9);
    }

    #[test]
    fn partial_bar_flushed_at_end() {
        // 7 M1 bars → one complete M5 + one partial M5 (2 bars)
        let feed = m1_feed(7);
        let mut agg = BarAggregator::new(feed, Timeframe::M5);
        let bars: Vec<Bar> = std::iter::from_fn(|| agg.next()).collect();
        assert_eq!(bars.len(), 2);
        assert!((bars[1].volume - 20.0).abs() < 1e-9); // 2 bars × 10
    }

    #[test]
    fn standalone_aggregator() {
        let mut agg = StandaloneAggregator::new(Timeframe::M5);
        let mut completed = Vec::new();
        for i in 0..10i64 {
            let bar = make_bar(i * 60_000, 100.0, 101.0, 99.0, 100.5, 10.0);
            if let Some(b) = agg.update(&bar) {
                completed.push(b);
            }
        }
        // Flush the last partial period
        if let Some(b) = agg.flush() {
            completed.push(b);
        }
        assert_eq!(completed.len(), 2);
    }

    #[test]
    fn reset_clears_state() {
        let feed = m1_feed(5);
        let mut agg = BarAggregator::new(feed, Timeframe::M5);
        let _first: Vec<Bar> = std::iter::from_fn(|| agg.next()).collect();
        agg.reset();
        let second: Vec<Bar> = std::iter::from_fn(|| agg.next()).collect();
        assert_eq!(second.len(), 1);
    }

    #[test]
    fn ohlcv_correct() {
        // 3 M1 bars with distinct prices → aggregate into one M3 bar
        let bars = vec![
            make_bar(0,         100.0, 105.0, 98.0,  101.0, 10.0),
            make_bar(60_000,    101.0, 107.0, 99.0,  103.0, 20.0),
            make_bar(120_000,   103.0, 110.0, 97.0,  102.0, 30.0),
        ];
        let feed = BarVecFeed::new(bars, "SYM".into());
        let mut agg = BarAggregator::new(feed, Timeframe::M3);
        let bar = agg.next().unwrap();
        assert_eq!(bar.open,   100.0);
        assert_eq!(bar.high,   110.0);
        assert_eq!(bar.low,     97.0);
        assert_eq!(bar.close,  102.0);
        assert!((bar.volume - 60.0).abs() < 1e-9);
    }
}
