use std::collections::VecDeque;

/// Weighted Moving Average — linear weights (most recent bar has highest weight).
///
/// WMA = (n·C₀ + (n−1)·C₁ + … + 1·Cₙ₋₁) / (n·(n+1)/2)
pub struct Wma {
    period: usize,
    buffer: VecDeque<f64>,
    weight_sum: f64, // n*(n+1)/2
}

impl Wma {
    pub fn new(period: usize) -> Self {
        assert!(period > 0);
        let weight_sum = (period * (period + 1) / 2) as f64;
        Self {
            period,
            buffer: VecDeque::with_capacity(period),
            weight_sum,
        }
    }

    pub fn update(&mut self, value: f64) -> Option<f64> {
        self.buffer.push_back(value);
        if self.buffer.len() > self.period {
            self.buffer.pop_front();
        }
        if self.buffer.len() < self.period {
            return None;
        }
        let wma = self
            .buffer
            .iter()
            .enumerate()
            .map(|(i, &v)| v * (i + 1) as f64)
            .sum::<f64>()
            / self.weight_sum;
        Some(wma)
    }

    pub fn is_ready(&self) -> bool {
        self.buffer.len() >= self.period
    }

    pub fn reset(&mut self) {
        self.buffer.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_wma_basic() {
        // WMA(3): weights [1,2,3], divisor = 6
        // Values [1,2,3] → (1*1 + 2*2 + 3*3) / 6 = 14/6
        let mut wma = Wma::new(3);
        assert_eq!(wma.update(1.0), None);
        assert_eq!(wma.update(2.0), None);
        let v = wma.update(3.0).unwrap();
        assert!((v - 14.0 / 6.0).abs() < 1e-9, "WMA = {v}");
    }

    #[test]
    fn test_wma_rolling() {
        // Values [2,3,4] → (2*1 + 3*2 + 4*3) / 6 = 20/6
        let mut wma = Wma::new(3);
        wma.update(1.0);
        wma.update(2.0);
        wma.update(3.0);
        let v = wma.update(4.0).unwrap();
        assert!((v - 20.0 / 6.0).abs() < 1e-9, "WMA rolling = {v}");
    }

    #[test]
    fn test_wma_constant_equals_value() {
        // All same values → WMA should equal that value
        let mut wma = Wma::new(5);
        let mut last = None;
        for _ in 0..5 {
            last = wma.update(42.0);
        }
        assert!((last.unwrap() - 42.0).abs() < 1e-9);
    }
}
