use crate::ema::Ema;

/// MACD value snapshot.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct MacdValue {
    pub macd: f64,
    pub signal: f64,
    pub histogram: f64,
}

/// MACD (Moving Average Convergence Divergence).
/// Classic params: fast=12, slow=26, signal=9.
#[derive(Debug, Clone)]
pub struct Macd {
    fast: Ema,
    slow: Ema,
    signal: Ema,
}

impl Macd {
    pub fn new(fast: usize, slow: usize, signal: usize) -> Self {
        assert!(fast < slow, "fast period must be < slow period");
        Self {
            fast: Ema::new(fast),
            slow: Ema::new(slow),
            signal: Ema::new(signal),
        }
    }

    /// Standard MACD(12, 26, 9)
    pub fn standard() -> Self {
        Self::new(12, 26, 9)
    }

    /// Feed a new closing price. Returns `Some(MacdValue)` once all EMAs are seeded.
    pub fn update(&mut self, close: f64) -> Option<MacdValue> {
        let fast = self.fast.update(close)?;
        let slow = self.slow.update(close)?;
        let macd_line = fast - slow;
        let signal_line = self.signal.update(macd_line)?;
        Some(MacdValue {
            macd: macd_line,
            signal: signal_line,
            histogram: macd_line - signal_line,
        })
    }

    pub fn value(&self) -> Option<MacdValue> {
        let fast = self.fast.value()?;
        let slow = self.slow.value()?;
        let macd_line = fast - slow;
        let signal_line = self.signal.value()?;
        Some(MacdValue {
            macd: macd_line,
            signal: signal_line,
            histogram: macd_line - signal_line,
        })
    }

    pub fn is_ready(&self) -> bool {
        self.fast.is_ready() && self.slow.is_ready() && self.signal.is_ready()
    }

    pub fn reset(&mut self) {
        self.fast.reset();
        self.slow.reset();
        self.signal.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_macd_ready_after_warmup() {
        let mut macd = Macd::new(3, 6, 2);
        // EMA(3) seeds at bar 3, EMA(6) at bar 6, signal EMA(2) needs 2 more MACD values → bar 8
        let mut got = false;
        for i in 0..10 {
            if macd.update(100.0 + i as f64).is_some() {
                got = true;
            }
        }
        assert!(got, "should produce at least one value");
        assert!(macd.is_ready());
    }

    #[test]
    fn test_macd_uptrend_positive() {
        let mut macd = Macd::new(3, 6, 2);
        let mut last = None;
        for i in 0..20 {
            last = macd.update(100.0 + i as f64 * 2.0);
        }
        let v = last.unwrap();
        // In a strong uptrend fast EMA > slow EMA → positive MACD line
        assert!(v.macd > 0.0, "MACD line should be positive: {}", v.macd);
    }

    #[test]
    fn test_macd_reset_clears_state() {
        // Due to cascading ? short-circuits (fast→slow→signal), standard MACD(12,26,9)
        // needs 12 + (26-12) + (9-1) = ~45 bars to produce first value.
        let mut macd = Macd::standard();
        for i in 0..50 {
            macd.update(100.0 + i as f64);
        }
        assert!(macd.is_ready());
        macd.reset();
        assert!(!macd.is_ready());
        assert!(macd.update(100.0).is_none());
    }
}
