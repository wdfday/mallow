//! Multi-Timeframe (MTF) bar aggregation for named strategies.
//!
//! # How MTF works
//!
//! The engine runs on a single base timeframe (e.g. M1). MTF is handled
//! **inside the strategy** — not at the engine or ledger layer. Each named
//! strategy that needs a higher timeframe creates a [`TimeBarResampler`] and
//! feeds every base-TF bar into it:
//!
//! ```text
//! Engine: on_bar(m1_bar)
//!   └─> resampler.push(m1_bar) → None        (same H4 bucket, accumulate)
//!   └─> resampler.push(m1_bar) → None
//!   ...
//!   └─> resampler.push(m1_bar) → Some(h4_bar)  ← first bar of next bucket
//!                                                  emits the completed bucket
//!         └─> htf_indicator.update(h4_bar)
//! ```
//!
//! The HTF bar is emitted **one base-TF bar late**: the H4 bar whose bucket
//! started at 08:00 is emitted when the first M1 bar of 12:00 arrives, not at
//! 11:59. This is intentional — it prevents any lookahead bias.
//!
//! # Timestamp of the emitted bar
//!
//! [`TimeBarResampler`] stamps the HTF bar with the **bucket floor** timestamp
//! (e.g. `08:00:00` for an H4 bucket starting at 08:00). Compare with
//! [`HtfAggregator`][crate::script] inside `ScriptStrategy`, which stamps with
//! the **last M1 bar's timestamp** inside that bucket.
//!
//! # Partial-bucket drop
//!
//! The forming (open) bucket is never emitted automatically. If a backtest ends
//! mid-bucket the last partial HTF bar is silently dropped. Use
//! [`TimeBarResampler::peek`] if the strategy needs the live forming bar.
//!
//! # Session boundary (stocks)
//!
//! If two consecutive base bars are more than 22 hours apart the resampler
//! treats it as an overnight gap and resets its bucket state. This prevents
//! the pre-market open of one session from merging with the close of the
//! previous session.
//!
//! # Scripts
//!
//! Script strategies do **not** use [`TimeBarResampler`] directly. Instead each
//! HTF indicator binding contains an inline [`HtfAggregator`] — same bucket
//! logic but with additional `live_` support. See
//! [`crate::script::strategy`] for details.

use alm_core::Bar;

// ── Supported MTF timeframes by asset class ───────────────────────────────────

/// Timeframes supported for Multi-Timeframe (MTF) indicators on **US stocks**.
///
/// H4 is intentionally excluded: the US equity session is 390 min (9:30–16:00),
/// which 240 min does not divide evenly.  Wall-clock H4 alignment produces a
/// ~2.5 h "stub" bar at the open of every session, making indicators computed on
/// it unreliable.  Use D1 as the higher timeframe instead.
///
/// Valid: `M1 M5 M15 M30 H1 H2 D1 W1`
pub const SUPPORTED_MTF_STOCK: &[&str] = &[
    "M1", "M5", "M15", "M30",
    "H1", "H2",
    "D1", "W1",
];

/// Timeframes supported for Multi-Timeframe (MTF) indicators on **crypto**.
///
/// Crypto trades 24/7 so all hourly multiples align cleanly. H4 is included
/// (6 full bars per day, 0 h / 4 h / 8 h / 12 h / 16 h / 20 h UTC).
///
/// Valid: `M1 M5 M15 M30 H1 H2 H4 D1 W1`
pub const SUPPORTED_MTF_CRYPTO: &[&str] = &[
    "M1", "M5", "M15", "M30",
    "H1", "H2", "H4",
    "D1", "W1",
];

// ── Time-based resampler ──────────────────────────────────────────────────────

/// Parse a TradingView-style timeframe string into milliseconds.
///
/// Accepts any `M<n>`, `H<n>`, `D<n>`, `W<n>` string where `n > 0`.
/// The parser is intentionally permissive — callers that need to enforce
/// asset-class restrictions should validate against [`SUPPORTED_MTF_STOCK`]
/// or [`SUPPORTED_MTF_CRYPTO`] before calling this function.
///
/// Returns `None` for unrecognised strings (bad unit letter, non-numeric
/// suffix, or `n == 0`).
pub fn parse_timeframe_ms(tf: &str) -> Option<i64> {
    let tf = tf.trim();
    if tf.len() < 2 { return None; }
    let (unit, digits) = tf.split_at(1);
    let n: i64 = digits.parse().ok()?;
    if n <= 0 { return None; }
    let ms = match unit {
        "M" => n * 60_000,
        "H" => n * 3_600_000,
        "D" => n * 86_400_000,
        "W" => n * 604_800_000,
        _ => return None,
    };
    Some(ms)
}

/// Time-based bar resampler — aggregates bars by wall-clock bucket.
///
/// Unlike [`BarResampler`] (count-based), this aligns to real time boundaries:
/// `floor(timestamp / interval_ms)` determines the bucket.
///
/// A completed HTF bar is emitted when the **first bar of the next bucket**
/// arrives, so the last in-progress bar is held until a new bucket opens.
/// This means the very last partial HTF bar is silently dropped — correct
/// behaviour for live trading (never trade on an incomplete candle).
///
/// ## Session gaps (stocks)
///
/// Session gaps (e.g. 16:00 → 9:30 next day) are handled transparently: the
/// overnight gap simply keeps the current bucket alive until the next bar
/// arrives, at which point the bucket boundary logic kicks in normally.
/// Bars are never merged across sessions.
///
/// ## Stub bar at session open (stocks, known limitation)
///
/// Because bucket boundaries are wall-clock-aligned (e.g. 9:00, 10:00, …),
/// but the US equity session opens at 9:30, the **first HTF bar of each
/// session is shorter than nominal**:
///
/// | HTF | Nominal | Actual (first bar) |
/// |-----|---------|--------------------|
/// | H1  | 60 min  | ~30 min (9:30–10:00) |
/// | H2  | 120 min | ~30 min (9:30–10:00) |
///
/// For smooth indicators (EMA, RSI) this is an acceptable imperfection.
/// It would matter more for ATR-based dynamic sizing (slightly underestimated
/// volatility at the open).  If session-accurate alignment becomes necessary,
/// add a `session_open: Option<NaiveTime>` field and align buckets to session
/// start instead of UTC midnight.
///
/// H4 is excluded from [`SUPPORTED_MTF_STOCK`] because 390-minute sessions
/// don't divide evenly by 240 min — the stub there is ~2.5 h, far too large.
///
/// ## OHLCV aggregation
/// - `timestamp` = bucket open time (floor of the first bar's timestamp)
/// - `open`   = open of the first bar in the bucket
/// - `high`   = max(high) across all bars
/// - `low`    = min(low) across all bars
/// - `close`  = close of the last bar in the bucket
/// - `volume` = sum(volume) across all bars
#[derive(Debug, Clone)]
pub struct TimeBarResampler {
    interval_ms: i64,
    bucket:      Option<i64>,   // current open-bucket timestamp
    open:        f64,
    high:        f64,
    low:         f64,
    close:       f64,
    volume:      f64,
    symbol:      String,
}

impl TimeBarResampler {
    pub fn new(interval_ms: i64) -> Self {
        assert!(interval_ms > 0, "TimeBarResampler interval_ms must be > 0");
        Self {
            interval_ms,
            bucket: None,
            open:   0.0,
            high:   f64::NEG_INFINITY,
            low:    f64::INFINITY,
            close:  0.0,
            volume: 0.0,
            symbol: String::new(),
        }
    }

    /// Feed one base bar.
    ///
    /// Returns `Some(htf_bar)` when the previous bucket is complete (i.e.
    /// the current bar belongs to a new bucket).  Returns `None` while
    /// accumulating within the current bucket.
    pub fn push(&mut self, bar: &Bar) -> Option<Bar> {
        let bucket = bar.timestamp - (bar.timestamp % self.interval_ms);

        match self.bucket {
            None => {
                // First bar ever — open a new bucket, nothing to emit yet.
                self.start(bucket, bar);
                None
            }
            Some(cur) if cur == bucket => {
                // Same bucket — accumulate.
                self.high   = self.high.max(bar.high);
                self.low    = self.low.min(bar.low);
                self.close  = bar.close;
                self.volume += bar.volume;
                None
            }
            Some(cur) => {
                // New bucket — emit the completed bar, then start fresh.
                let completed = Bar::new(
                    cur,
                    self.symbol.as_str(),
                    self.open,
                    self.high,
                    self.low,
                    self.close,
                    self.volume,
                );
                self.start(bucket, bar);
                Some(completed)
            }
        }
    }

    pub fn interval_ms(&self) -> i64 { self.interval_ms }

    /// Returns the bar currently being accumulated (the "forming" / live bar)
    /// without emitting or advancing state.
    ///
    /// Returns `None` if no base bar has been received yet.
    pub fn peek(&self) -> Option<Bar> {
        self.bucket.map(|ts| Bar::new(
            ts,
            &self.symbol,
            self.open,
            self.high,
            self.low,
            self.close,
            self.volume,
        ))
    }

    pub fn reset(&mut self) {
        self.bucket = None;
        self.volume = 0.0;
        self.high   = f64::NEG_INFINITY;
        self.low    = f64::INFINITY;
    }

    fn start(&mut self, bucket: i64, bar: &Bar) {
        self.bucket = Some(bucket);
        self.open   = bar.open;
        self.high   = bar.high;
        self.low    = bar.low;
        self.close  = bar.close;
        self.volume = bar.volume;
        self.symbol = bar.symbol.clone();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // ── parse_timeframe_ms ────────────────────────────────────────────────────

    #[test]
    fn test_parse_timeframe_ms() {
        assert_eq!(parse_timeframe_ms("M1"),  Some(60_000));
        assert_eq!(parse_timeframe_ms("M15"), Some(900_000));
        assert_eq!(parse_timeframe_ms("H1"),  Some(3_600_000));
        assert_eq!(parse_timeframe_ms("H4"),  Some(14_400_000));
        assert_eq!(parse_timeframe_ms("D1"),  Some(86_400_000));
        assert_eq!(parse_timeframe_ms("W1"),  Some(604_800_000));
        assert_eq!(parse_timeframe_ms("X1"),  None);
        assert_eq!(parse_timeframe_ms("H"),   None);
        assert_eq!(parse_timeframe_ms(""),    None);
    }

    // ── TimeBarResampler ──────────────────────────────────────────────────────

    fn tbar(ts_ms: i64, c: f64) -> Bar {
        Bar::new(ts_ms, "X", c, c + 1.0, c - 1.0, c, 100.0)
    }

    #[test]
    fn time_resampler_emits_on_bucket_change() {
        // H1 = 3_600_000 ms.  Feed 3 M20 bars (at :00, :20, :40), then one
        // bar in the next hour (:60) — expect the H1 bar to be emitted.
        let hour = 3_600_000i64;
        let mut rs = TimeBarResampler::new(hour);

        assert!(rs.push(&tbar(0 * 60_000, 10.0)).is_none());   // :00
        assert!(rs.push(&tbar(20 * 60_000, 12.0)).is_none());  // :20
        assert!(rs.push(&tbar(40 * 60_000, 11.0)).is_none());  // :40

        // First bar of next hour — triggers emission of the H1 bar.
        let h1 = rs.push(&tbar(60 * 60_000, 13.0)).unwrap();   // 01:00
        assert_eq!(h1.timestamp, 0);          // bucket open time
        assert_eq!(h1.open,  10.0);
        assert!((h1.high - 13.0).abs() < 1e-9); // max(11,13,12) = 13
        assert!((h1.low  -  9.0).abs() < 1e-9); // min(9,11,10)  = 9
        assert_eq!(h1.close, 11.0);              // close of last bar in bucket
        assert!((h1.volume - 300.0).abs() < 1e-9);
    }

    #[test]
    fn time_resampler_no_cross_session() {
        // Last M1 bar of day ends at 15:59, next day starts at 09:30.
        // With H1 resampler these must NOT be merged.
        let hour = 3_600_000i64;
        let mut rs = TimeBarResampler::new(hour);

        // 15:00 bucket
        rs.push(&tbar(15 * hour, 100.0));
        rs.push(&tbar(15 * hour + 30 * 60_000, 101.0));
        // 9:30 next day — new bucket; previous (15h) bar emitted
        let emitted = rs.push(&tbar(24 * hour + 9 * hour + 30 * 60_000, 200.0));
        assert!(emitted.is_some(), "H1 bar from 15h should be emitted");
        let b = emitted.unwrap();
        assert_eq!(b.timestamp, 15 * hour, "bucket open = 15:00");
        assert_eq!(b.open, 100.0);
    }

    #[test]
    fn time_resampler_reset_clears_bucket() {
        let hour = 3_600_000i64;
        let mut rs = TimeBarResampler::new(hour);
        rs.push(&tbar(0, 100.0));
        rs.reset();
        // After reset the bucket is gone; next bar opens a new one with no emission.
        assert!(rs.push(&tbar(hour, 200.0)).is_none());
    }
}
