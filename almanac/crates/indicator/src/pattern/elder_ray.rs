use crate::Ema;

#[derive(Debug, Clone, Copy)]
pub struct ElderRayValue {
    pub ema: f64,
    /// Bull Power = High - EMA (positive = bulls in control)
    pub bull_power: f64,
    /// Bear Power = Low - EMA  (negative = bears in control)
    pub bear_power: f64,
}

/// Elder Ray index — separates the power of bulls from bears.
///
/// Entry: EMA uptrend + Bear Power < 0 but rising → long.
/// Entry: EMA downtrend + Bull Power > 0 but falling → short.
#[derive(Debug, Clone)]
pub struct ElderRay {
    ema: Ema,
    period: usize,
}

impl ElderRay {
    pub fn new(period: usize) -> Self {
        Self { ema: Ema::new(period), period }
    }

    pub fn update(&mut self, high: f64, low: f64, close: f64) -> Option<ElderRayValue> {
        let ema = self.ema.update(close)?;
        Some(ElderRayValue {
            ema,
            bull_power: high - ema,
            bear_power: low - ema,
        })
    }

    pub fn reset(&mut self) {
        self.ema = Ema::new(self.period);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_elder_ray_signs() {
        let mut er = ElderRay::new(3);
        er.update(11.0, 9.0, 10.0);
        er.update(12.0, 10.0, 11.0);
        let v = er.update(13.0, 11.0, 12.0).unwrap();
        // In uptrend, close > ema so bull_power could be negative or positive depending on H vs EMA
        assert!(v.bull_power >= v.bear_power); // Bull >= Bear always (high >= low)
    }
}
