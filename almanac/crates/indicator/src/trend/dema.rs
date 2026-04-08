use crate::Ema;

/// Double Exponential Moving Average — reduces EMA lag.
/// DEMA(n) = 2 * EMA(n) - EMA(EMA(n))
#[derive(Debug, Clone)]
pub struct Dema {
    ema1: Ema,
    ema2: Ema,
    period: usize,
}

impl Dema {
    pub fn new(period: usize) -> Self {
        Self {
            ema1: Ema::new(period),
            ema2: Ema::new(period),
            period,
        }
    }

    pub fn update(&mut self, value: f64) -> Option<f64> {
        let e1 = self.ema1.update(value)?;
        let e2 = self.ema2.update(e1)?;
        Some(2.0 * e1 - e2)
    }

    pub fn value(&self) -> Option<f64> {
        let e1 = self.ema1.value()?;
        let e2 = self.ema2.value()?;
        Some(2.0 * e1 - e2)
    }

    pub fn reset(&mut self) {
        self.ema1 = Ema::new(self.period);
        self.ema2 = Ema::new(self.period);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_dema_warmup() {
        let mut d = Dema::new(3);
        // Needs 2*period bars to fully warm up both EMAs
        for i in 0..5 {
            d.update(i as f64 + 1.0);
        }
        assert!(d.value().is_some());
    }

    #[test]
    fn test_dema_less_lag_than_ema() {
        // DEMA should react faster: on a perfect uptrend DEMA > EMA
        let mut dema = Dema::new(5);
        let mut ema = Ema::new(5);
        let mut dema_val = 0.0;
        let mut ema_val = 0.0;
        for i in 1..=20 {
            if let Some(v) = dema.update(i as f64) { dema_val = v; }
            if let Some(v) = ema.update(i as f64) { ema_val = v; }
        }
        assert!(dema_val > ema_val, "DEMA should lead EMA on uptrend");
    }
}
