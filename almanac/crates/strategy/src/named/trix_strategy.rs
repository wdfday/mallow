use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Trix;

/// Bot — TRIX Signal Line Crossover.
///
/// Long when TRIX line crosses above its signal line.
/// Close when TRIX crosses below signal line.
pub struct TrixStrategy {
    trix: Trix,
    prev_hist: Option<f64>,
    in_position: bool,
    period: usize,
    signal_period: usize,
}

impl TrixStrategy {
    pub fn new(period: usize, signal_period: usize) -> Self {
        Self {
            trix: Trix::new(period, signal_period),
            prev_hist: None,
            in_position: false,
            period,
            signal_period,
        }
    }
}

impl Strategy for TrixStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.trix.update(bar.close) else {
            return vec![];
        };

        let prev = self.prev_hist.replace(v.histogram);
        let Some(p) = prev else {
            return vec![];
        };

        // Histogram crosses above 0: TRIX crossed above signal
        if p <= 0.0 && v.histogram > 0.0 && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if p >= 0.0 && v.histogram < 0.0 && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "trix"
    }

    fn reset(&mut self) {
        self.trix = Trix::new(self.period, self.signal_period);
        self.prev_hist = None;
        self.in_position = false;
    }
}
