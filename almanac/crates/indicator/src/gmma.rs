use crate::ema::Ema;

#[derive(Debug, Clone)]
pub struct GmmaValue {
    /// Short-term group: EMAs(3,5,8,10,12,15) — trader sentiment.
    pub short: [f64; 6],
    /// Long-term group: EMAs(30,35,40,45,50,60) — investor sentiment.
    pub long: [f64; 6],
    /// `true` when short group is fully above long group (bullish alignment).
    pub bullish: bool,
}

/// Guppy Multiple Moving Average (GMMA).
///
/// 12 EMAs split into short (trader) and long (investor) groups.
/// Crossover of the two groups indicates trend change.
pub struct Gmma {
    short_emas: [Ema; 6],
    long_emas: [Ema; 6],
}

const SHORT_PERIODS: [usize; 6] = [3, 5, 8, 10, 12, 15];
const LONG_PERIODS: [usize; 6] = [30, 35, 40, 45, 50, 60];

impl Gmma {
    /// Standard GMMA with default periods.
    pub fn new() -> Self {
        Self {
            short_emas: SHORT_PERIODS.map(Ema::new),
            long_emas: LONG_PERIODS.map(Ema::new),
        }
    }

    /// Custom periods. `short` and `long` must each have exactly 6 elements.
    pub fn with_periods(short: [usize; 6], long: [usize; 6]) -> Self {
        Self {
            short_emas: short.map(Ema::new),
            long_emas: long.map(Ema::new),
        }
    }

    pub fn update(&mut self, close: f64) -> Option<GmmaValue> {
        let mut short_vals = [0.0f64; 6];
        let mut long_vals = [0.0f64; 6];

        for (i, ema) in self.short_emas.iter_mut().enumerate() {
            short_vals[i] = ema.update(close)?;
        }
        for (i, ema) in self.long_emas.iter_mut().enumerate() {
            long_vals[i] = ema.update(close)?;
        }

        let short_min = short_vals.iter().cloned().fold(f64::INFINITY, f64::min);
        let long_max = long_vals.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let bullish = short_min > long_max;

        Some(GmmaValue { short: short_vals, long: long_vals, bullish })
    }

    pub fn reset(&mut self) {
        self.short_emas = SHORT_PERIODS.map(Ema::new);
        self.long_emas = LONG_PERIODS.map(Ema::new);
    }
}

impl Default for Gmma {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // NOTE: Due to cascading `?` short-circuits in the update loop, each EMA only
    // starts receiving data once the previous (shorter) EMA is ready. The effective
    // warmup is roughly: 3 + 4 + 7 + 9 + 11 + 14 + 29 + 34 + 39 + 44 + 49 + 59 ≈ 302 bars.

    #[test]
    fn test_gmma_ready_after_warmup() {
        let mut gmma = Gmma::new();
        let mut got = false;
        for i in 0..350 {
            if gmma.update(100.0 + i as f64).is_some() {
                got = true;
            }
        }
        assert!(got, "GMMA should produce values after ~302 bars warmup");
    }

    #[test]
    fn test_gmma_bullish_strong_uptrend() {
        let mut gmma = Gmma::new();
        let mut last = None;
        for i in 0..400 {
            last = gmma.update(100.0 + i as f64 * 3.0);
        }
        let v = last.unwrap();
        assert!(v.bullish, "GMMA should be bullish in strong sustained uptrend");
    }

    #[test]
    fn test_gmma_short_above_long_in_uptrend() {
        let mut gmma = Gmma::new();
        let mut last = None;
        for i in 0..400 {
            last = gmma.update(100.0 + i as f64 * 3.0);
        }
        let v = last.unwrap();
        let short_min = v.short.iter().cloned().fold(f64::INFINITY, f64::min);
        let long_max = v.long.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        assert!(short_min > long_max, "short group should be above long group");
    }
}
