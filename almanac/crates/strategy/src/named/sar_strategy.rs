use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::ParabolicSar;

/// Bot — Parabolic SAR trend follower.
///
/// Long when SAR flips bullish (price crosses above SAR).
/// Close when SAR flips bearish.
pub struct SarStrategy {
    sar: ParabolicSar,
    step: f64,
    max: f64,
    prev_bullish: Option<bool>,
    in_position: bool,
}

impl SarStrategy {
    pub fn new(step: f64, max: f64) -> Self {
        Self {
            sar: ParabolicSar::new(step, max),
            step,
            max,
            prev_bullish: None,
            in_position: false,
        }
    }
}

impl Strategy for SarStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.sar.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let prev = self.prev_bullish.replace(v.is_bullish);
        let Some(was_bullish) = prev else {
            return vec![];
        };

        if v.is_bullish && !was_bullish && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.is_bullish && was_bullish && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "parabolic_sar"
    }

    fn reset(&mut self) {
        self.sar = ParabolicSar::new(self.step, self.max);
        self.prev_bullish = None;
        self.in_position = false;
    }
}
