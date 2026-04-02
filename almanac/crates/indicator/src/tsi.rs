use crate::Ema;

/// True Strength Index (TSI).
///
/// Double-smoothed momentum indicator:
///   pc    = close - prev_close
///   PCDS  = EMA(second, EMA(first, pc))
///   APCDS = EMA(second, EMA(first, |pc|))
///   TSI   = (PCDS / APCDS) * 100
///
/// Range: -100..+100, 0 = neutral
/// Defaults: first=25, second=13
#[derive(Debug, Clone)]
pub struct Tsi {
    first: usize,
    second: usize,
    prev_close: Option<f64>,
    /// First-pass EMA of price change
    ema1_pc: Ema,
    /// Second-pass EMA of first-pass EMA (price change)
    ema2_pc: Ema,
    /// First-pass EMA of |price change|
    ema1_apc: Ema,
    /// Second-pass EMA of first-pass |price change|
    ema2_apc: Ema,
    value: Option<f64>,
}

impl Tsi {
    pub fn new(first: usize, second: usize) -> Self {
        assert!(first > 0 && second > 0, "TSI periods must be > 0");
        Self {
            first,
            second,
            prev_close: None,
            ema1_pc: Ema::new(first),
            ema2_pc: Ema::new(second),
            ema1_apc: Ema::new(first),
            ema2_apc: Ema::new(second),
            value: None,
        }
    }

    /// Default parameters: first=25, second=13
    pub fn default() -> Self {
        Self::new(25, 13)
    }

    /// Feed a new closing price. Returns `Some(tsi)` once double-smoothed series is ready.
    pub fn update(&mut self, close: f64) -> Option<f64> {
        let Some(prev) = self.prev_close else {
            self.prev_close = Some(close);
            return None;
        };
        self.prev_close = Some(close);

        let pc = close - prev;
        let apc = pc.abs();

        // Chain the two EMAs
        if let Some(e1) = self.ema1_pc.update(pc) {
            if let Some(pcds) = self.ema2_pc.update(e1) {
                // We only need the apcds when pcds is ready; ema1_apc runs in parallel
                if let Some(e1a) = self.ema1_apc.update(apc) {
                    if let Some(apcds) = self.ema2_apc.update(e1a) {
                        self.value = if apcds.abs() < f64::EPSILON {
                            Some(0.0)
                        } else {
                            Some((pcds / apcds) * 100.0)
                        };
                        return self.value;
                    }
                    // ema2_apc not ready yet
                    return None;
                }
                // ema1_apc not ready — still need to feed apc even though we got pcds
                return None;
            }
        }
        // ema1_apc may still need feeding even if ema1_pc not ready
        self.ema1_apc.update(apc);
        None
    }

    pub fn value(&self) -> Option<f64> {
        self.value
    }

    pub fn is_ready(&self) -> bool {
        self.value.is_some()
    }

    pub fn reset(&mut self) {
        self.prev_close = None;
        self.ema1_pc = Ema::new(self.first);
        self.ema2_pc = Ema::new(self.second);
        self.ema1_apc = Ema::new(self.first);
        self.ema2_apc = Ema::new(self.second);
        self.value = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_tsi_not_ready_during_warmup() {
        let mut tsi = Tsi::new(5, 3);
        for i in 0..8 {
            let v = tsi.update(100.0 + i as f64);
            assert!(v.is_none(), "TSI should not be ready during warmup: bar {i}");
        }
    }

    #[test]
    fn test_tsi_ready_after_warmup() {
        let mut tsi = Tsi::new(5, 3);
        // Need prev_close + first period + second period = 1 + 5 + 3 - 1 = 8 bars minimum
        for i in 0..20 {
            tsi.update(100.0 + i as f64 * 0.5);
        }
        assert!(tsi.is_ready(), "TSI should be ready after sufficient bars");
    }

    #[test]
    fn test_tsi_uptrend_positive() {
        let mut tsi = Tsi::new(5, 3);
        for i in 0..50 {
            tsi.update(100.0 + i as f64);
        }
        let v = tsi.value().unwrap();
        assert!(v > 0.0, "TSI should be positive in strong uptrend: {v}");
    }

    #[test]
    fn test_tsi_range_bounded() {
        let mut tsi = Tsi::new(5, 3);
        for i in 0..50 {
            tsi.update(100.0 + (i % 3) as f64 * 10.0);
        }
        if let Some(v) = tsi.value() {
            assert!(v >= -100.0 && v <= 100.0, "TSI out of range: {v}");
        }
    }

    #[test]
    fn test_tsi_reset() {
        let mut tsi = Tsi::new(5, 3);
        for i in 0..30 {
            tsi.update(100.0 + i as f64);
        }
        assert!(tsi.is_ready());
        tsi.reset();
        assert!(!tsi.is_ready());
    }
}
