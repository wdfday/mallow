use crate::Ema;

/// Average True Range — measures volatility.
///
/// TR = max(high - low, |high - prev_close|, |low - prev_close|)
/// ATR = EMA(TR, period)
#[derive(Debug, Clone)]
pub struct Atr {
    _period: usize,
    ema: Ema,
    prev_close: Option<f64>,
}

/// ATR output: true_range and smoothed atr value.
#[derive(Debug, Clone, Copy)]
pub struct AtrValue {
    pub tr: f64,
    pub atr: f64,
}

impl Atr {
    pub fn new(period: usize) -> Self {
        Self {
            _period: period,
            ema: Ema::new(period),
            prev_close: None,
        }
    }

    /// Standard ATR(14)
    pub fn standard() -> Self {
        Self::new(14)
    }

    /// Feed high, low, close. Returns ATR once ready.
    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<AtrValue> {
        let tr = match self.prev_close {
            Some(pc) => {
                let hl = high - low;
                let hc = (high - pc).abs();
                let lc = (low - pc).abs();
                hl.max(hc).max(lc)
            }
            None => high - low,
        };
        self.prev_close = Some(close);

        self.ema.update(tr).map(|atr| AtrValue { tr, atr })
    }

    pub fn value(&self) -> Option<f64> {
        self.ema.value()
    }

    pub fn is_ready(&self) -> bool {
        self.ema.is_ready()
    }

    pub fn reset(&mut self) {
        self.ema.reset();
        self.prev_close = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_atr_basic() {
        let mut atr = Atr::new(3);
        // Bar 1: no prev_close, TR = H - L
        assert!(atr.update(12.0, 10.0, 11.0).is_none());
        // Bar 2
        assert!(atr.update(13.0, 10.5, 12.0).is_none());
        // Bar 3 — should be ready
        let v = atr.update(14.0, 11.0, 13.0);
        assert!(v.is_some());
        assert!(v.unwrap().atr > 0.0);
    }
}
