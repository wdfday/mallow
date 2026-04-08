use std::collections::VecDeque;

/// Rate of Change — measures percentage change over `period` bars.
///
/// ROC = ((Close − Close[n]) / Close[n]) × 100
///
/// Positive ROC → momentum up; negative → momentum down.
#[derive(Debug, Clone)]
pub struct Roc {
    period: usize,
    buffer: VecDeque<f64>,
}

impl Roc {
    pub fn new(period: usize) -> Self {
        assert!(period > 0);
        Self {
            period,
            buffer: VecDeque::with_capacity(period + 1),
        }
    }

    pub fn update(&mut self, close: f64) -> Option<f64> {
        self.buffer.push_back(close);
        if self.buffer.len() > self.period + 1 {
            self.buffer.pop_front();
        }
        if self.buffer.len() < self.period + 1 {
            return None;
        }
        let past = self.buffer[0];
        if past == 0.0 {
            return Some(0.0);
        }
        Some((close - past) / past * 100.0)
    }

    pub fn is_ready(&self) -> bool {
        self.buffer.len() >= self.period + 1
    }

    pub fn reset(&mut self) {
        self.buffer.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_roc_basic() {
        // ROC(3): close now vs close 3 bars ago
        // 100 → 100 → 100 → 110 → ROC = (110-100)/100 * 100 = 10.0
        let mut roc = Roc::new(3);
        assert!(roc.update(100.0).is_none());
        assert!(roc.update(100.0).is_none());
        assert!(roc.update(100.0).is_none());
        let v = roc.update(110.0).unwrap();
        assert!((v - 10.0).abs() < 1e-9, "ROC = {v}");
    }

    #[test]
    fn test_roc_negative() {
        let mut roc = Roc::new(2);
        roc.update(100.0);
        roc.update(100.0);
        let v = roc.update(90.0).unwrap();
        assert!((v - (-10.0)).abs() < 1e-9, "ROC = {v}");
    }

    #[test]
    fn test_roc_zero_return() {
        let mut roc = Roc::new(2);
        roc.update(100.0);
        roc.update(100.0);
        let v = roc.update(100.0).unwrap();
        assert!((v - 0.0).abs() < 1e-9);
    }
}
