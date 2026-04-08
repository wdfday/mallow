use std::collections::VecDeque;

/// Volatility Ratio (Schwager).
///
/// VR = True Range / (Highest High − Lowest Low) over `lookback` bars.
///
/// Values near 1.0 mean today's range spans the whole lookback range (explosive move).
/// Values near 0.0 mean today's range is tiny relative to recent history.
/// Typical breakout threshold: VR > 0.5.
pub struct VolatilityRatio {
    lookback: usize,
    highs: VecDeque<f64>,
    lows: VecDeque<f64>,
    prev_close: Option<f64>,
}

impl VolatilityRatio {
    pub fn new(lookback: usize) -> Self {
        assert!(lookback >= 2);
        Self {
            lookback,
            highs: VecDeque::with_capacity(lookback),
            lows: VecDeque::with_capacity(lookback),
            prev_close: None,
        }
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<f64> {
        let tr = match self.prev_close {
            Some(pc) => (high - low).max((high - pc).abs()).max((low - pc).abs()),
            None => high - low,
        };
        self.prev_close = Some(close);

        self.highs.push_back(high);
        self.lows.push_back(low);

        if self.highs.len() > self.lookback {
            self.highs.pop_front();
            self.lows.pop_front();
        }

        if self.highs.len() < self.lookback {
            return None;
        }

        let hh = self.highs.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let ll = self.lows.iter().cloned().fold(f64::INFINITY, f64::min);
        let range = hh - ll;

        if range == 0.0 {
            return Some(0.0);
        }

        Some(tr / range)
    }

    pub fn reset(&mut self) {
        self.highs.clear();
        self.lows.clear();
        self.prev_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_vr_bounds() {
        let mut vr = VolatilityRatio::new(5);
        let mut last = None;
        for i in 0..10 {
            let p = 100.0 + i as f64;
            last = vr.update(p + 1.5, p - 1.5, p);
        }
        let v = last.unwrap();
        assert!(v >= 0.0, "VR must be non-negative: {v}");
    }

    #[test]
    fn test_vr_flat_market_near_zero() {
        // All same prices → TR tiny but range also tiny → can be near 1 or 0
        let mut vr = VolatilityRatio::new(5);
        let mut last = None;
        for _ in 0..10 {
            last = vr.update(100.0, 100.0, 100.0);
        }
        // Flat: TR = 0, range = 0 → returns 0.0
        assert_eq!(last.unwrap(), 0.0);
    }

    #[test]
    fn test_vr_explosive_move_near_one() {
        // One big move after flat consolidation → TR ≈ full range → VR near 1
        let mut vr = VolatilityRatio::new(5);
        for _ in 0..4 {
            vr.update(100.0, 99.0, 99.5);
        }
        // Explosive bar that spans entire 5-bar range
        let v = vr.update(110.0, 90.0, 100.0).unwrap();
        assert!(v > 0.5, "explosive move VR should be > 0.5: {v}");
    }
}
