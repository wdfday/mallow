use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Rwi;

/// Bot — Random Walk Index (Schwager).
///
/// Long when RWI_High > `threshold` (non-random uptrend detected).
/// Close when RWI_Low > `threshold` (non-random downtrend).
pub struct RwiStrategy {
    rwi: Rwi,
    threshold: f64,
    period: usize,
}

impl RwiStrategy {
    pub fn new(period: usize, threshold: f64) -> Self {
        Self {
            rwi: Rwi::new(period),
            threshold,
            period,
        }
    }
}

impl Strategy for RwiStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.rwi.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if v.rwi_high > self.threshold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, (v.rwi_high - 1.0).min(1.0).max(0.0))];
        }
        if v.rwi_low > self.threshold {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "rwi"
    }

    fn reset(&mut self) {
        self.rwi = Rwi::new(self.period);
    }
}
