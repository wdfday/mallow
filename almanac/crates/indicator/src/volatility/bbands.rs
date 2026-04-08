use std::collections::VecDeque;

/// Bollinger Bands value snapshot.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct BBandsValue {
    pub middle: f64,
    pub upper: f64,
    pub lower: f64,
    /// Bandwidth: (upper - lower) / middle
    pub bandwidth: f64,
    /// %B: (price - lower) / (upper - lower)
    pub percent_b: f64,
}

/// Bollinger Bands — SMA ± k * stddev.
/// Classic params: period=20, k=2.0.
#[derive(Debug, Clone)]
pub struct BBands {
    period: usize,
    k: f64,
    buffer: VecDeque<f64>,
    sum: f64,
}

impl BBands {
    pub fn new(period: usize, k: f64) -> Self {
        assert!(period > 1, "BBands period must be > 1");
        Self {
            period,
            k,
            buffer: VecDeque::with_capacity(period + 1),
            sum: 0.0,
        }
    }

    /// Standard BBands(20, 2.0)
    pub fn standard() -> Self {
        Self::new(20, 2.0)
    }

    pub fn update(&mut self, value: f64) -> Option<BBandsValue> {
        self.buffer.push_back(value);
        self.sum += value;

        if self.buffer.len() > self.period {
            self.sum -= self.buffer.pop_front().unwrap();
        }

        if self.buffer.len() < self.period {
            return None;
        }

        let mean = self.sum / self.period as f64;
        let variance =
            self.buffer.iter().map(|x| (x - mean).powi(2)).sum::<f64>() / self.period as f64;
        let stddev = variance.sqrt();

        let upper = mean + self.k * stddev;
        let lower = mean - self.k * stddev;
        let bandwidth = if mean.abs() > f64::EPSILON {
            (upper - lower) / mean
        } else {
            0.0
        };
        let range = upper - lower;
        let percent_b = if range.abs() > f64::EPSILON {
            (value - lower) / range
        } else {
            0.5
        };

        Some(BBandsValue {
            middle: mean,
            upper,
            lower,
            bandwidth,
            percent_b,
        })
    }

    pub fn is_ready(&self) -> bool {
        self.buffer.len() == self.period
    }

    pub fn reset(&mut self) {
        self.buffer.clear();
        self.sum = 0.0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_bbands_symmetry() {
        let mut bb = BBands::new(3, 2.0);
        bb.update(10.0);
        bb.update(10.0);
        let v = bb.update(10.0).unwrap();
        // All same values → stddev = 0 → upper = lower = middle
        assert!((v.upper - 10.0).abs() < 1e-9);
        assert!((v.lower - 10.0).abs() < 1e-9);
    }
}
