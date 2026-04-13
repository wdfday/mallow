use alm_core::Bar;

/// Gom N bar nhỏ thành 1 bar lớn hơn (count-based resampling).
///
/// Dùng cho Multi-Timeframe (MTF): strategy nhận M5 bars nhưng một số
/// indicator cần tính trên H1 (factor=12) hoặc H4 (factor=48).
///
/// # Cách dùng
/// ```text
/// let mut rs = BarResampler::new(4);   // mỗi 4 bar M15 → 1 bar H1
/// for bar in m15_bars {
///     if let Some(h1) = rs.push(&bar) {
///         h1_indicator.update(h1.high, h1.low, h1.close);
///     }
/// }
/// ```
///
/// # OHLCV aggregation
/// - `open`  = open của bar đầu tiên trong window
/// - `high`  = max(high) trong window
/// - `low`   = min(low) trong window
/// - `close` = close của bar cuối cùng trong window
/// - `volume`= sum(volume) trong window
/// - `timestamp` = timestamp của bar cuối cùng (bar đóng cửa)
#[derive(Debug, Clone)]
pub struct BarResampler {
    factor: usize,
    count: usize,
    open: f64,
    high: f64,
    low: f64,
    close: f64,
    volume: f64,
    timestamp: i64,
    symbol: String,
}

impl BarResampler {
    pub fn new(factor: usize) -> Self {
        assert!(factor >= 2, "BarResampler factor must be >= 2");
        Self {
            factor,
            count: 0,
            open: 0.0,
            high: f64::NEG_INFINITY,
            low: f64::INFINITY,
            close: 0.0,
            volume: 0.0,
            timestamp: 0,
            symbol: String::new(),
        }
    }

    /// Feed một bar. Trả về `Some(aggregated_bar)` khi đủ `factor` bars.
    pub fn push(&mut self, bar: &Bar) -> Option<Bar> {
        if self.count == 0 {
            // Đầu window mới
            self.open = bar.open;
            self.high = bar.high;
            self.low = bar.low;
            self.symbol = bar.symbol.clone();
        } else {
            self.high = self.high.max(bar.high);
            self.low = self.low.min(bar.low);
        }

        self.close = bar.close;
        self.volume += bar.volume;
        self.timestamp = bar.timestamp;
        self.count += 1;

        if self.count == self.factor {
            let agg = Bar::new(
                self.timestamp,
                self.symbol.as_str(),
                self.open,
                self.high,
                self.low,
                self.close,
                self.volume,
            );
            self.count = 0;
            self.volume = 0.0;
            self.high = f64::NEG_INFINITY;
            self.low = f64::INFINITY;
            Some(agg)
        } else {
            None
        }
    }

    pub fn factor(&self) -> usize {
        self.factor
    }

    pub fn reset(&mut self) {
        self.count = 0;
        self.volume = 0.0;
        self.high = f64::NEG_INFINITY;
        self.low = f64::INFINITY;
    }
}

// ── Supported MTF timeframes by asset class ───────────────────────────────────

/// Timeframes supported for Multi-Timeframe (MTF) indicators on **US stocks**.
///
/// H4 is intentionally excluded: the US equity session is 390 min (9:30–16:00),
/// which 240 min does not divide evenly.  Wall-clock H4 alignment produces a
/// ~2.5 h "stub" bar at the open of every session, making indicators computed on
/// it unreliable.  Use D1 as the higher timeframe instead.
///
/// Valid: `M1 M5 M15 M30 H1 H2 H3 D1 W1`
pub const SUPPORTED_MTF_STOCK: &[&str] = &[
    "M1", "M5", "M15", "M30",
    "H1", "H2", "H3",
    "D1", "W1",
];

/// Timeframes supported for Multi-Timeframe (MTF) indicators on **crypto**.
///
/// Crypto trades 24/7 so all hourly multiples align cleanly. H4 is included
/// (6 full bars per day, 0 h / 4 h / 8 h / 12 h / 16 h / 20 h UTC).
///
/// Valid: `M1 M5 M15 M30 H1 H2 H3 H4 D1 W1`
pub const SUPPORTED_MTF_CRYPTO: &[&str] = &[
    "M1", "M5", "M15", "M30",
    "H1", "H2", "H3", "H4",
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
/// | H3  | 180 min | ~30 min (9:30–10:00) |
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

    fn bar(i: i64, c: f64) -> Bar {
        Bar::new(i, "X", c, c + 1.0, c - 1.0, c, 100.0)
    }

    #[test]
    fn test_aggregates_ohlcv() {
        let mut rs = BarResampler::new(3);
        assert!(rs.push(&bar(1, 10.0)).is_none());
        assert!(rs.push(&bar(2, 12.0)).is_none());
        let agg = rs.push(&bar(3, 11.0)).unwrap();
        assert_eq!(agg.open, 10.0);
        assert!((agg.high - 13.0).abs() < 1e-9); // max(11,13,12) = 13
        assert!((agg.low - 9.0).abs() < 1e-9);   // min(9,11,10)  = 9
        assert_eq!(agg.close, 11.0);
        assert!((agg.volume - 300.0).abs() < 1e-9);
        assert_eq!(agg.timestamp, 3);
    }

    #[test]
    fn test_resets_after_complete() {
        let mut rs = BarResampler::new(2);
        rs.push(&bar(1, 10.0));
        let a1 = rs.push(&bar(2, 20.0)).unwrap();
        rs.push(&bar(3, 30.0));
        let a2 = rs.push(&bar(4, 40.0)).unwrap();
        assert_eq!(a1.open, 10.0);
        assert_eq!(a2.open, 30.0); // new window starts at bar 3
    }

    #[test]
    fn test_reset_clears_state() {
        let mut rs = BarResampler::new(4);
        rs.push(&bar(1, 100.0));
        rs.push(&bar(2, 101.0));
        rs.reset();
        // After reset, next bar starts fresh window
        rs.push(&bar(3, 200.0));
        let agg = rs.push(&bar(4, 201.0));
        assert!(agg.is_none()); // factor=4, only 2 bars fed after reset
    }

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
