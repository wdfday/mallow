use std::collections::VecDeque;

/// Choppiness Index (CHOP).
///
/// Measures whether the market is trending or ranging:
///   CHOP = 100 * log10(SUM(ATR(1), n) / (MAX(high, n) - MIN(low, n))) / log10(n)
///
/// Range: ~0..100
///   > 61.8 = choppy / ranging
///   < 38.2 = strongly trending
///
/// Default: period=14
///
/// Note: ATR(1) here means the 1-period true range (not smoothed).
#[derive(Debug, Clone)]
pub struct Chop {
    period: usize,
    /// Rolling window: (high, low, close) tuples — need period+1 to compute TR
    bars: VecDeque<(f64, f64, f64)>,
    value: Option<f64>,
}

impl Chop {
    pub fn new(period: usize) -> Self {
        assert!(period > 1, "Chop period must be > 1");
        Self {
            period,
            // +1 because we need prev_close to compute TR for the first bar in the window
            bars: VecDeque::with_capacity(period + 2),
            value: None,
        }
    }

    /// Default parameters: period=14
    pub fn default() -> Self {
        Self::new(14)
    }

    /// Feed high, low, close. Returns `Some(chop)` after `period + 1` bars.
    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<f64> {
        self.bars.push_back((high, low, close));

        // Keep period + 1 bars (need one extra for TR of oldest bar)
        while self.bars.len() > self.period + 1 {
            self.bars.pop_front();
        }

        if self.bars.len() < self.period + 1 {
            return None;
        }

        // Compute sum of true ranges and high/low range over the last `period` bars
        let mut sum_tr = 0.0;
        let mut period_high = f64::MIN;
        let mut period_low = f64::MAX;

        // bars[0] is the previous bar (only used as prev_close for bars[1])
        // bars[1..=period] are the period bars we compute over
        for i in 1..=self.period {
            let (h, l, _c) = self.bars[i];
            let prev_close = self.bars[i - 1].2;

            let hl = h - l;
            let hc = (h - prev_close).abs();
            let lc = (l - prev_close).abs();
            let tr = hl.max(hc).max(lc);

            sum_tr += tr;
            if h > period_high {
                period_high = h;
            }
            if l < period_low {
                period_low = l;
            }
        }

        let range = period_high - period_low;
        if range < f64::EPSILON || sum_tr < f64::EPSILON {
            self.value = Some(100.0); // max choppiness when no movement
            return self.value;
        }

        let log_n = (self.period as f64).log10();
        let chop = 100.0 * (sum_tr / range).log10() / log_n;
        self.value = Some(chop.clamp(0.0, 100.0));
        self.value
    }

    pub fn value(&self) -> Option<f64> {
        self.value
    }

    pub fn is_ready(&self) -> bool {
        self.value.is_some()
    }

    pub fn reset(&mut self) {
        self.bars.clear();
        self.value = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_chop_warmup() {
        let mut chop = Chop::new(5);
        for i in 0..5 {
            let v = chop.update(100.0 + i as f64, 99.0 + i as f64, 100.0 + i as f64);
            assert!(v.is_none(), "should not be ready during warmup: bar {i}");
        }
        let v = chop.update(105.0, 104.0, 105.0);
        assert!(v.is_some(), "should be ready after period+1 bars");
    }

    #[test]
    fn test_chop_range_bounded() {
        let mut chop = Chop::new(5);
        for i in 0..30 {
            let base = 100.0 + (i % 5) as f64;
            if let Some(v) = chop.update(base + 1.0, base - 1.0, base) {
                assert!(v >= 0.0 && v <= 100.0, "CHOP out of range: {v}");
            }
        }
    }

    #[test]
    fn test_chop_trending_market() {
        let mut chop = Chop::new(5);
        // Strong trend → CHOP should be low (< 50)
        let mut last = None;
        for i in 0..30 {
            let base = 100.0 + i as f64 * 2.0;
            last = chop.update(base + 1.0, base - 0.5, base);
        }
        let v = last.unwrap();
        assert!(v < 62.0, "CHOP should be low in strong trend: {v}");
    }

    #[test]
    fn test_chop_choppy_market() {
        let mut chop = Chop::new(5);
        // Sideways volatile market → CHOP should be high (> 50)
        let prices = [100.0, 105.0, 98.0, 104.0, 99.0, 103.0, 97.0, 104.0, 100.0, 105.0];
        let mut last = None;
        for &p in &prices {
            last = chop.update(p + 2.0, p - 2.0, p);
        }
        let v = last.unwrap();
        assert!(v > 50.0, "CHOP should be high in sideways market: {v}");
    }

    #[test]
    fn test_chop_reset() {
        let mut chop = Chop::new(5);
        for i in 0..15 {
            chop.update(100.0 + i as f64, 99.0 + i as f64, 100.0 + i as f64);
        }
        assert!(chop.is_ready());
        chop.reset();
        assert!(!chop.is_ready());
    }
}
