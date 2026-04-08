//! Awesome Oscillator (AO)
//!
//! Median Price = (High + Low) / 2
//! AO = SMA(5, median) - SMA(34, median)
//!
//! Above zero = bullish momentum, below zero = bearish.

use std::collections::VecDeque;

struct Sma {
    period: usize,
    buf: VecDeque<f64>,
    sum: f64,
}

impl Sma {
    fn new(period: usize) -> Self {
        Self { period, buf: VecDeque::with_capacity(period), sum: 0.0 }
    }
    fn update(&mut self, v: f64) -> Option<f64> {
        self.buf.push_back(v);
        self.sum += v;
        if self.buf.len() > self.period {
            self.sum -= self.buf.pop_front().unwrap();
        }
        if self.buf.len() == self.period {
            Some(self.sum / self.period as f64)
        } else {
            None
        }
    }
    fn reset(&mut self) {
        self.buf.clear();
        self.sum = 0.0;
    }
}

pub struct AwesomeOscillator {
    fast: Sma,
    slow: Sma,
    fast_period: usize,
    slow_period: usize,
    // Buffer to align fast and slow SMA outputs
    fast_buf: VecDeque<f64>,
}

impl AwesomeOscillator {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        Self {
            fast: Sma::new(fast_period),
            slow: Sma::new(slow_period),
            fast_period,
            slow_period,
            fast_buf: VecDeque::new(),
        }
    }

    pub fn update(&mut self, high: f64, low: f64) -> Option<f64> {
        let median = (high + low) / 2.0;
        let fast_val = self.fast.update(median);
        let slow_val = self.slow.update(median);

        // Fast SMA starts outputting at fast_period, slow at slow_period.
        // We need to skip fast values until slow is ready.
        if let Some(fv) = fast_val {
            self.fast_buf.push_back(fv);
        }
        let skip = self.slow_period - self.fast_period;
        if self.fast_buf.len() > skip + 1 {
            self.fast_buf.pop_front();
        }

        if let (Some(_), Some(sv)) = (fast_val, slow_val) {
            let aligned_fast = *self.fast_buf.front()?;
            self.fast_buf.pop_front();
            Some(aligned_fast - sv)
        } else {
            None
        }
    }

    pub fn reset(&mut self) {
        self.fast.reset();
        self.slow.reset();
        self.fast_buf.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_warmup() {
        let mut ao = AwesomeOscillator::new(5, 34);
        for i in 0..34 {
            let v = ao.update(105.0, 95.0);
            if i < 33 {
                assert!(v.is_none());
            } else {
                assert!(v.is_some());
            }
        }
    }

    #[test]
    fn test_flat_market() {
        let mut ao = AwesomeOscillator::new(5, 34);
        let mut last = None;
        for _ in 0..50 { last = ao.update(105.0, 95.0); }
        // Flat market → AO should be ~0
        assert!(last.unwrap().abs() < 0.001);
    }

    #[test]
    fn test_reset() {
        let mut ao = AwesomeOscillator::new(5, 34);
        for _ in 0..40 { ao.update(105.0, 95.0); }
        ao.reset();
        assert!(ao.update(105.0, 95.0).is_none());
    }
}
