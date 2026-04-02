use std::collections::VecDeque;

/// Simple Moving Average — O(1) update via rolling sum.
#[derive(Debug, Clone)]
pub struct Sma {
    period: usize,
    buffer: VecDeque<f64>,
    sum: f64,
}

impl Sma {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "SMA period must be > 0");
        Self { period, buffer: VecDeque::with_capacity(period + 1), sum: 0.0 }
    }

    /// Feed a new value. Returns `Some(sma)` once the buffer is full.
    pub fn update(&mut self, value: f64) -> Option<f64> {
        self.buffer.push_back(value);
        self.sum += value;

        if self.buffer.len() > self.period {
            self.sum -= self.buffer.pop_front().unwrap();
        }

        if self.buffer.len() == self.period {
            Some(self.sum / self.period as f64)
        } else {
            None
        }
    }

    pub fn value(&self) -> Option<f64> {
        if self.buffer.len() == self.period {
            Some(self.sum / self.period as f64)
        } else {
            None
        }
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
    fn test_sma_basic() {
        let mut sma = Sma::new(3);
        assert_eq!(sma.update(1.0), None);
        assert_eq!(sma.update(2.0), None);
        assert_eq!(sma.update(3.0), Some(2.0));
        assert_eq!(sma.update(4.0), Some(3.0));
        assert_eq!(sma.update(5.0), Some(4.0));
    }
}
