//! Triple Exponential Moving Average (TEMA)
//!
//! TEMA = (3 * EMA1) - (3 * EMA2) + EMA3
//! where EMA1 = EMA(close), EMA2 = EMA(EMA1), EMA3 = EMA(EMA2)
//!
//! Reduces lag more aggressively than DEMA.

use crate::Ema;

pub struct Tema {
    ema1: Ema,
    ema2: Ema,
    ema3: Ema,
}

impl Tema {
    pub fn new(period: usize) -> Self {
        Self {
            ema1: Ema::new(period),
            ema2: Ema::new(period),
            ema3: Ema::new(period),
        }
    }

    pub fn update(&mut self, close: f64) -> Option<f64> {
        let e1 = self.ema1.update(close)?;
        let e2 = self.ema2.update(e1)?;
        let e3 = self.ema3.update(e2)?;
        Some(3.0 * e1 - 3.0 * e2 + e3)
    }

    pub fn reset(&mut self) {
        self.ema1.reset();
        self.ema2.reset();
        self.ema3.reset();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_warmup() {
        let mut t = Tema::new(3);
        // Needs 3 EMA warmups = 3*(3-1) = 6 extra bars before first value
        for i in 0..8 {
            let v = t.update(100.0 + i as f64);
            if i < 6 {
                assert!(v.is_none(), "bar {i} should be None");
            }
        }
    }

    #[test]
    fn test_trend() {
        let mut t = Tema::new(3);
        let mut last = None;
        for i in 0..20 {
            last = t.update(100.0 + i as f64 * 2.0);
        }
        assert!(last.unwrap() > 100.0);
    }

    #[test]
    fn test_reset() {
        let mut t = Tema::new(3);
        for i in 0..15 { t.update(100.0 + i as f64); }
        t.reset();
        assert!(t.update(100.0).is_none());
    }
}
