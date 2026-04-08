use std::collections::VecDeque;

/// Kaufman's Adaptive Moving Average (KAMA).
///
/// Adapts its smoothing based on the Efficiency Ratio (ER):
///   ER         = |Close - Close[n]| / Sum(|Close - PrevClose|, n)
///   fast_sc    = 2 / (fast + 1)
///   slow_sc    = 2 / (slow + 1)
///   SC         = (ER * (fast_sc - slow_sc) + slow_sc)^2
///   KAMA       = prevKAMA + SC * (close - prevKAMA)
///
/// Defaults: er_period=10, fast=2, slow=30
#[derive(Debug, Clone)]
pub struct Kama {
    er_period: usize,
    fast_sc: f64,
    slow_sc: f64,
    /// Rolling window of closes for ER direction calculation
    closes: VecDeque<f64>,
    kama: Option<f64>,
}

impl Kama {
    pub fn new(er_period: usize, fast: usize, slow: usize) -> Self {
        assert!(er_period > 0, "KAMA er_period must be > 0");
        assert!(fast > 0 && slow > fast, "KAMA: fast must be < slow");
        Self {
            er_period,
            fast_sc: 2.0 / (fast as f64 + 1.0),
            slow_sc: 2.0 / (slow as f64 + 1.0),
            closes: VecDeque::with_capacity(er_period + 2),
            kama: None,
        }
    }

    /// Default parameters: er_period=10, fast=2, slow=30
    pub fn default() -> Self {
        Self::new(10, 2, 30)
    }

    /// Feed a new closing price. Returns `Some(kama)` after `er_period + 1` bars.
    pub fn update(&mut self, close: f64) -> Option<f64> {
        self.closes.push_back(close);

        // Keep only what we need: er_period + 1 closes
        while self.closes.len() > self.er_period + 1 {
            self.closes.pop_front();
        }

        if self.closes.len() < self.er_period + 1 {
            return None;
        }

        // Direction = |close - close[er_period ago]|
        let oldest = *self.closes.front().unwrap();
        let direction = (close - oldest).abs();

        // Volatility = sum of |close - prev_close| over er_period bars
        let volatility: f64 = self
            .closes
            .iter()
            .zip(self.closes.iter().skip(1))
            .map(|(a, b)| (b - a).abs())
            .sum();

        let er = if volatility < f64::EPSILON {
            0.0
        } else {
            (direction / volatility).clamp(0.0, 1.0)
        };

        let sc = (er * (self.fast_sc - self.slow_sc) + self.slow_sc).powi(2);

        let prev_kama = self.kama.unwrap_or(close);
        let new_kama = prev_kama + sc * (close - prev_kama);
        self.kama = Some(new_kama);
        self.kama
    }

    pub fn value(&self) -> Option<f64> {
        self.kama
    }

    pub fn is_ready(&self) -> bool {
        self.kama.is_some()
    }

    pub fn reset(&mut self) {
        self.closes.clear();
        self.kama = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_kama_warmup() {
        let mut kama = Kama::new(10, 2, 30);
        // Should not be ready until er_period+1 bars
        for i in 0..10 {
            let v = kama.update(100.0 + i as f64);
            assert!(v.is_none(), "should return None before warmup: bar {i}");
        }
        let v = kama.update(110.0);
        assert!(v.is_some(), "should return Some after warmup");
    }

    #[test]
    fn test_kama_follows_trend() {
        let mut kama = Kama::new(5, 2, 10);
        // Seed with flat prices
        for _ in 0..6 {
            kama.update(100.0);
        }
        let start = kama.value().unwrap();
        // Strong uptrend — KAMA should rise
        for i in 1..=10 {
            kama.update(100.0 + i as f64 * 2.0);
        }
        let end = kama.value().unwrap();
        assert!(end > start, "KAMA should rise in uptrend: start={start}, end={end}");
    }

    #[test]
    fn test_kama_no_panic_zero_volatility() {
        let mut kama = Kama::new(5, 2, 10);
        // All same price → zero volatility, should not panic
        for _ in 0..20 {
            kama.update(100.0);
        }
        let v = kama.value().unwrap();
        assert!((v - 100.0).abs() < 1e-9, "KAMA should stay at 100.0");
    }

    #[test]
    fn test_kama_reset() {
        let mut kama = Kama::new(5, 2, 10);
        for i in 0..10 {
            kama.update(100.0 + i as f64);
        }
        assert!(kama.is_ready());
        kama.reset();
        assert!(!kama.is_ready());
        assert!(kama.value().is_none());
    }
}
