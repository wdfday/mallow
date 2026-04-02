use crate::wma::Wma;
use std::collections::VecDeque;

/// Hull Moving Average — reduces lag while maintaining smoothness.
///
/// HMA(n) = WMA( 2·WMA(n/2) − WMA(n),  √n )
pub struct Hma {
    wma_half: Wma,
    wma_full: Wma,
    wma_sqrt: Wma,
    sqrt_period: usize,
    period: usize,
    // Buffer for the intermediate series fed into wma_sqrt
    intermediate: VecDeque<f64>,
}

impl Hma {
    pub fn new(period: usize) -> Self {
        assert!(period >= 4, "HMA period must be >= 4");
        let half = (period / 2).max(1);
        let sqrt_p = (period as f64).sqrt().round() as usize;
        Self {
            wma_half: Wma::new(half),
            wma_full: Wma::new(period),
            wma_sqrt: Wma::new(sqrt_p),
            sqrt_period: sqrt_p,
            period,
            intermediate: VecDeque::new(),
        }
    }

    pub fn update(&mut self, value: f64) -> Option<f64> {
        let half_val = self.wma_half.update(value);
        let full_val = self.wma_full.update(value);

        if let (Some(h), Some(f)) = (half_val, full_val) {
            let raw = 2.0 * h - f;
            self.wma_sqrt.update(raw)
        } else {
            None
        }
    }

    pub fn is_ready(&self) -> bool {
        self.wma_full.is_ready()
    }

    pub fn reset(&mut self) {
        self.wma_half = Wma::new(self.period / 2);
        self.wma_full = Wma::new(self.period);
        self.wma_sqrt = Wma::new(self.sqrt_period);
        self.intermediate.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_hma_produces_values() {
        let mut hma = Hma::new(16);
        let mut got = false;
        for i in 0..30 {
            if hma.update(100.0 + i as f64).is_some() {
                got = true;
            }
        }
        assert!(got, "HMA(16) should produce values within 30 bars");
    }

    #[test]
    fn test_hma_uptrend_rising() {
        let mut hma = Hma::new(9);
        let mut values = Vec::new();
        for i in 0..30 {
            if let Some(v) = hma.update(100.0 + i as f64) {
                values.push(v);
            }
        }
        assert!(values.len() >= 2);
        // HMA should be rising in a linear uptrend
        assert!(values.last() > values.first(), "HMA should rise in uptrend");
    }

    #[test]
    fn test_hma_reset() {
        let mut hma = Hma::new(9);
        for i in 0..30 {
            hma.update(100.0 + i as f64);
        }
        hma.reset();
        // After reset, should not emit a value immediately
        assert!(hma.update(100.0).is_none());
    }
}
