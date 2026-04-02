use std::collections::VecDeque;

/// Williams %R — momentum oscillator (-100 to 0).
///
/// %R = (highest_high - close) / (highest_high - lowest_low) * -100
///
/// -80 to -100: oversold
///   0 to -20: overbought
#[derive(Debug, Clone)]
pub struct WilliamsR {
    period: usize,
    highs: VecDeque<f64>,
    lows: VecDeque<f64>,
}

impl WilliamsR {
    pub fn new(period: usize) -> Self {
        assert!(period > 0);
        Self {
            period,
            highs: VecDeque::with_capacity(period + 1),
            lows: VecDeque::with_capacity(period + 1),
        }
    }

    /// Standard Williams %R(14)
    pub fn standard() -> Self {
        Self::new(14)
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<f64> {
        self.highs.push_back(high);
        self.lows.push_back(low);

        if self.highs.len() > self.period {
            self.highs.pop_front();
            self.lows.pop_front();
        }

        if self.highs.len() < self.period {
            return None;
        }

        let highest = self.highs.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let lowest = self.lows.iter().cloned().fold(f64::INFINITY, f64::min);
        let range = highest - lowest;

        if range < f64::EPSILON {
            Some(-50.0)
        } else {
            Some((highest - close) / range * -100.0)
        }
    }

    pub fn is_ready(&self) -> bool {
        self.highs.len() == self.period
    }

    pub fn reset(&mut self) {
        self.highs.clear();
        self.lows.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_williams_r_bounds() {
        let mut wr = WilliamsR::new(5);
        for i in 0..10 {
            let p = 100.0 + i as f64;
            if let Some(v) = wr.update(p + 1.0, p - 1.0, p) {
                assert!(v >= -100.0 && v <= 0.0, "%R={} out of bounds", v);
            }
        }
    }

    #[test]
    fn test_williams_r_extremes() {
        let mut wr = WilliamsR::new(3);
        // Create range 10..12, close at highest → %R near 0
        wr.update(12.0, 10.0, 11.0);
        wr.update(12.0, 10.0, 11.0);
        let v = wr.update(12.0, 10.0, 12.0).unwrap();
        assert!(
            (v - 0.0).abs() < 1.0,
            "Close at high should give %R near 0, got {v}"
        );
    }
}
