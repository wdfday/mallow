use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Roc;

/// Bot — ROC Zero-Cross.
///
/// Long when ROC crosses above 0 (positive momentum).
/// Close when ROC crosses below 0.
pub struct RocCrossover {
    roc: Roc,
    prev_roc: Option<f64>,
    in_position: bool,
    period: usize,
}

impl RocCrossover {
    pub fn new(period: usize) -> Self {
        Self {
            roc: Roc::new(period),
            prev_roc: None,
            in_position: false,
            period,
        }
    }
}

impl Strategy for RocCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.roc.update(bar.close) else {
            return vec![];
        };

        let prev = self.prev_roc.replace(v);
        let Some(p) = prev else {
            return vec![];
        };

        if p <= 0.0 && v > 0.0 && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if p >= 0.0 && v < 0.0 && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "roc"
    }

    fn reset(&mut self) {
        self.roc = Roc::new(self.period);
        self.prev_roc = None;
        self.in_position = false;
    }
}

