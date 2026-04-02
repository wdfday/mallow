use crate::{atr::Atr, ema::Ema};

#[derive(Debug, Clone)]
pub struct KeltnerValue {
    pub middle: f64,
    pub upper: f64,
    pub lower: f64,
}

/// Keltner Channel.
///
/// Middle = EMA(close, period)
/// Upper  = Middle + multiplier × ATR(atr_period)
/// Lower  = Middle − multiplier × ATR(atr_period)
pub struct Keltner {
    ema: Ema,
    atr: Atr,
    multiplier: f64,
    period: usize,
    atr_period: usize,
}

impl Keltner {
    pub fn new(period: usize, atr_period: usize, multiplier: f64) -> Self {
        Self {
            ema: Ema::new(period),
            atr: Atr::new(atr_period),
            multiplier,
            period,
            atr_period,
        }
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<KeltnerValue> {
        let mid = self.ema.update(close)?;
        let atr = self.atr.update(high, low, close)?;
        let band = self.multiplier * atr.atr;
        Some(KeltnerValue {
            middle: mid,
            upper: mid + band,
            lower: mid - band,
        })
    }

    pub fn reset(&mut self) {
        self.ema = Ema::new(self.period);
        self.atr = Atr::new(self.atr_period);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_keltner_symmetry() {
        let mut kc = Keltner::new(3, 3, 2.0);
        let mut last = None;
        for i in 0..10 {
            let p = 100.0 + i as f64;
            last = kc.update(p + 1.0, p - 1.0, p);
        }
        let v = last.unwrap();
        // Upper > middle > lower
        assert!(v.upper > v.middle, "upper > middle");
        assert!(v.middle > v.lower, "middle > lower");
    }

    #[test]
    fn test_keltner_constant_price_zero_atr() {
        let mut kc = Keltner::new(3, 3, 2.0);
        let mut last = None;
        for _ in 0..10 {
            last = kc.update(100.0, 100.0, 100.0);
        }
        let v = last.unwrap();
        // Zero ATR → upper = lower = middle
        assert!((v.upper - v.middle).abs() < 1e-9);
        assert!((v.lower - v.middle).abs() < 1e-9);
    }
}
