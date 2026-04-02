use crate::Sma;
use std::collections::VecDeque;

/// Stochastic Oscillator — %K and %D.
///
/// %K = (close - lowest_low) / (highest_high - lowest_low) * 100
/// %D = SMA(%K, d_period)
#[derive(Debug, Clone)]
pub struct Stochastic {
    k_period: usize,
    highs: VecDeque<f64>,
    lows: VecDeque<f64>,
    d_smooth: Sma,
}

#[derive(Debug, Clone, Copy)]
pub struct StochasticValue {
    pub k: f64,
    pub d: f64,
}

impl Stochastic {
    pub fn new(k_period: usize, d_period: usize) -> Self {
        Self {
            k_period,
            highs: VecDeque::with_capacity(k_period + 1),
            lows: VecDeque::with_capacity(k_period + 1),
            d_smooth: Sma::new(d_period),
        }
    }

    /// Standard Stochastic(14, 3)
    pub fn standard() -> Self {
        Self::new(14, 3)
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<StochasticValue> {
        self.highs.push_back(high);
        self.lows.push_back(low);

        if self.highs.len() > self.k_period {
            self.highs.pop_front();
            self.lows.pop_front();
        }

        if self.highs.len() < self.k_period {
            return None;
        }

        let highest = self.highs.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let lowest = self.lows.iter().cloned().fold(f64::INFINITY, f64::min);
        let range = highest - lowest;

        let k = if range > f64::EPSILON {
            (close - lowest) / range * 100.0
        } else {
            50.0 // flat market
        };

        self.d_smooth.update(k).map(|d| StochasticValue { k, d })
    }

    pub fn is_ready(&self) -> bool {
        self.d_smooth.is_ready()
    }

    pub fn reset(&mut self) {
        self.highs.clear();
        self.lows.clear();
        self.d_smooth.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_stochastic_bounds() {
        let mut stoch = Stochastic::new(5, 3);
        // Feed uptrend — %K should be near 100
        for i in 0..10 {
            let p = 100.0 + i as f64;
            if let Some(v) = stoch.update(p + 1.0, p - 1.0, p) {
                assert!(v.k >= 0.0 && v.k <= 100.0, "K={} out of bounds", v.k);
                assert!(v.d >= 0.0 && v.d <= 100.0, "D={} out of bounds", v.d);
            }
        }
    }
}
