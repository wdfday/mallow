//! Forming-bucket aggregator for the v2 "clone-and-project" live model.
//!
//! Differs from [`v1::HtfAggregator`][crate::script::v1] in one key way: it
//! **never emits a completed bar**. Instead it just accumulates OHLCV of the
//! currently-forming HTF bucket using incoming base bars, exposes a `peek()`
//! reconstruction of that forming bar, and resets when its owner observes a
//! real HTF feed bar (signalling that the bucket has actually closed).
//!
//! ```text
//!   base bar arrives
//!         │
//!         ▼
//!   accumulate(bar)
//!     bucket_start = floor(bar.t, tf_ms) on first bar of a fresh bucket
//!     o/h/l/c/v   maintained
//!     fill_ratio  = (base_bars_seen / expected_bars_per_bucket).min(1)
//!         │
//!         ▼
//!   peek() → Bar { t: bucket_start, ohlcv: forming so far }
//!
//!   HTF feed bar arrives (real H1 from feed)
//!         │
//!         ▼
//!   reset()  — start fresh for the next bucket
//! ```

use alm_core::Bar;

/// Forming-bucket OHLCV accumulator for one HTF binding.
///
/// Holds no notion of indicator state — that lives on `FeedVarBinding`.
/// Owner clones the indicator and feeds it [`Self::peek`] to compute the
/// "live" value.
#[derive(Debug, Clone)]
pub(super) struct LiveBucketAggregator {
    /// HTF interval in ms (e.g. 3_600_000 for H1).
    tf_ms: i64,
    /// Base-TF interval in ms (e.g. 60_000 for M1). Used for `fill_ratio`.
    base_tf_ms: i64,
    bucket_start: Option<i64>,
    bars_in_bucket: u32,
    o: f64,
    h: f64,
    l: f64,
    c: f64,
    v: f64,
    symbol: String,
}

impl LiveBucketAggregator {
    pub(super) fn new(tf_ms: i64, base_tf_ms: i64) -> anyhow::Result<Self> {
        anyhow::ensure!(tf_ms > 0 && base_tf_ms > 0, "intervals must be positive");
        anyhow::ensure!(
            tf_ms >= base_tf_ms,
            "HTF interval ({tf_ms} ms) must be ≥ base TF interval ({base_tf_ms} ms); \
             declaring an indicator on a timeframe smaller than the base TF is not supported"
        );
        // `fill_ratio` divides `tf_ms / base_tf_ms` to get the expected bar
        // count per bucket — if that's not exact (e.g. base M3 + HTF M5:
        // 300_000 / 180_000 truncates to 1 instead of the true ~1.67), the
        // integer division silently under-counts and `fill_ratio` reports
        // "bucket fully formed" far too early.
        anyhow::ensure!(
            tf_ms % base_tf_ms == 0,
            "HTF interval ({tf_ms} ms) must be an exact multiple of the base TF interval \
             ({base_tf_ms} ms) — e.g. M5 doesn't evenly divide into M3. Pick a base TF that \
             evenly divides every declared HTF."
        );
        Ok(Self {
            tf_ms,
            base_tf_ms,
            bucket_start: None,
            bars_in_bucket: 0,
            o: 0.0, h: 0.0, l: 0.0, c: 0.0, v: 0.0,
            symbol: String::new(),
        })
    }

    /// Fold one base bar into the current forming bucket. When the bar
    /// belongs to a new bucket, the previous forming state is silently
    /// discarded — owners should call [`Self::reset`] explicitly when a
    /// real confirmed HTF feed bar arrives so the two paths stay in sync.
    pub(super) fn accumulate(&mut self, bar: &Bar) {
        let new_bucket = bar.timestamp.div_euclid(self.tf_ms) * self.tf_ms;
        let same_bucket = self.bucket_start == Some(new_bucket);
        if !same_bucket {
            // Start a fresh bucket. We do NOT emit anything — the v2 model
            // says: confirmed values come from the HTF feed, not from
            // aggregation.
            self.bucket_start = Some(new_bucket);
            self.o = bar.open;
            self.h = bar.high;
            self.l = bar.low;
            self.c = bar.close;
            self.v = bar.volume;
            self.bars_in_bucket = 1;
            self.symbol = bar.symbol.clone();
        } else {
            self.h = self.h.max(bar.high);
            self.l = self.l.min(bar.low);
            self.c = bar.close;
            self.v += bar.volume;
            self.bars_in_bucket += 1;
        }
    }

    /// Reconstruct the forming HTF bar. `timestamp` = bucket-open. Returns
    /// `None` until the first base bar has been seen.
    pub(super) fn peek(&self) -> Option<Bar> {
        self.bucket_start.map(|ts| Bar::new(
            ts,
            self.symbol.as_str(),
            self.o, self.h, self.l, self.c, self.v,
        ))
    }

    /// Fraction of the current bucket that has been observed (0..=1).
    /// Useful for UI display ("H1 is 75% formed").
    pub(super) fn fill_ratio(&self) -> f64 {
        let expected = (self.tf_ms / self.base_tf_ms.max(1)).max(1) as f64;
        (self.bars_in_bucket as f64 / expected).min(1.0)
    }

    /// Return the timeframe duration in milliseconds.
    pub(super) fn tf_ms(&self) -> i64 {
        self.tf_ms
    }

    /// Clear forming state. Called when a real HTF feed bar arrives — the
    /// bucket is now closed for real, so the live projection resets.
    pub(super) fn reset(&mut self) {
        self.bucket_start = None;
        self.bars_in_bucket = 0;
        self.o = 0.0;
        self.h = 0.0;
        self.l = 0.0;
        self.c = 0.0;
        self.v = 0.0;
        self.symbol.clear();
    }
}

#[cfg(test)]
mod tests {
    use crate::test_utils::*;
    use super::*;
    use alm_core::Timeframe;

    fn b(t: i64, o: f64, h: f64, l: f64, c: f64, v: f64) -> Bar {
        Bar::new(t, "T", o, h, l, c, v)
    }

    #[test]
    fn rejects_htf_not_a_multiple_of_base_tf() {
        // M5 (300_000 ms) doesn't evenly divide into M3 (180_000 ms) —
        // `fill_ratio`'s `tf_ms / base_tf_ms` would silently truncate
        // 1.666... down to 1, under-counting the expected bars per bucket.
        let err = LiveBucketAggregator::new(
            Timeframe::M5.duration_ms(),
            Timeframe::M3.duration_ms(),
        ).err().expect("M5 HTF over M3 base must be rejected");
        assert!(err.to_string().contains("exact multiple"), "unexpected error: {err}");
    }

    #[test]
    fn accepts_htf_exact_multiple_of_base_tf() {
        // M15 (900_000 ms) is exactly 5x M3 (180_000 ms).
        assert!(LiveBucketAggregator::new(
            Timeframe::M15.duration_ms(),
            Timeframe::M3.duration_ms(),
        ).is_ok());
    }

    #[test]
    fn peek_returns_none_until_first_bar() {
        let agg = LiveBucketAggregator::new(
            Timeframe::H1.duration_ms(),
            Timeframe::M1.duration_ms(),
        ).unwrap();
        assert!(agg.peek().is_none());
        assert_eq!(agg.fill_ratio(), 0.0);
    }

    #[test]
    fn forming_bucket_ohlc_correct() {
        let h1 = Timeframe::H1.duration_ms();
        let m1 = Timeframe::M1.duration_ms();
        let mut agg = LiveBucketAggregator::new(h1, m1).unwrap();
        // 3 M1 bars all inside the same H1 bucket (starting at 0):
        agg.accumulate(&b(0,        100.0, 102.0, 99.0, 101.0, 10.0));
        agg.accumulate(&b(60_000,   101.0, 105.0, 100.0, 104.0, 20.0));
        agg.accumulate(&b(120_000,  104.0, 106.0, 103.0, 103.5, 15.0));
        let forming = agg.peek().unwrap();
        assert_eq!(forming.open,  100.0);
        assert!((forming.high - 106.0).abs() < 1e-9);
        assert!((forming.low  -  99.0).abs() < 1e-9);
        assert!((forming.close - 103.5).abs() < 1e-9);
        assert!((forming.volume - 45.0).abs() < 1e-9);
        assert_eq!(forming.timestamp, 0, "bucket open = 0");
    }

    #[test]
    fn fill_ratio_caps_at_one() {
        let h1 = Timeframe::H1.duration_ms();
        let m1 = Timeframe::M1.duration_ms();
        let mut agg = LiveBucketAggregator::new(h1, m1).unwrap();
        for i in 0..30 {
            agg.accumulate(&b(i * 60_000, 100.0, 100.0, 100.0, 100.0, 1.0));
        }
        assert!((agg.fill_ratio() - 0.5).abs() < 1e-9, "30/60 = 0.5");
        for i in 30..120 {
            agg.accumulate(&b(i * 60_000, 100.0, 100.0, 100.0, 100.0, 1.0));
        }
        // Even past expected 60 we cap at 1.0 (also note new bucket starts at 60).
        assert!(agg.fill_ratio() <= 1.0 + 1e-9);
    }

    #[test]
    fn new_bucket_resets_forming_state() {
        let h1 = Timeframe::H1.duration_ms();
        let m1 = Timeframe::M1.duration_ms();
        let mut agg = LiveBucketAggregator::new(h1, m1).unwrap();
        // Bucket 0..60
        agg.accumulate(&b(30 * 60_000, 100.0, 110.0, 90.0, 105.0, 50.0));
        // Bucket 60..120 (different bucket)
        agg.accumulate(&b(60 * 60_000, 50.0, 51.0, 49.0, 50.5, 1.0));
        let forming = agg.peek().unwrap();
        // Forming reflects ONLY the new bucket; previous values discarded.
        assert_eq!(forming.timestamp, 60 * 60_000);
        assert_eq!(forming.open,  50.0);
        assert!((forming.high - 51.0).abs() < 1e-9);
        assert!((forming.low  - 49.0).abs() < 1e-9);
        assert!((forming.close - 50.5).abs() < 1e-9);
        assert!((forming.volume - 1.0).abs() < 1e-9);
    }

    #[test]
    fn reset_clears_state() {
        let h1 = Timeframe::H1.duration_ms();
        let m1 = Timeframe::M1.duration_ms();
        let mut agg = LiveBucketAggregator::new(h1, m1).unwrap();
        agg.accumulate(&b(0, 100.0, 102.0, 99.0, 101.0, 10.0));
        agg.reset();
        assert!(agg.peek().is_none());
        assert_eq!(agg.fill_ratio(), 0.0);
    }
}
