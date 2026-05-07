use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Fisher;

/// Fisher Transform signal line crossover.
///
/// Long when Fisher crosses above its signal line from below.
/// Close when Fisher crosses below its signal line.
pub struct FisherCrossover {
    fisher: Fisher,
    prev_fisher: Option<f64>,
    prev_signal: Option<f64>,
    in_position: bool,
    period: usize,
}

impl FisherCrossover {
    pub fn new(period: usize) -> Self {
        Self {
            fisher: Fisher::new(period),
            prev_fisher: None,
            prev_signal: None,
            in_position: false,
            period,
        }
    }
}

impl Strategy for FisherCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(fv) = self.fisher.update(bar.high, bar.low) else {
            return vec![];
        };

        let (Some(pf), Some(ps)) = (self.prev_fisher, self.prev_signal) else {
            self.prev_fisher = Some(fv.fisher);
            self.prev_signal = Some(fv.signal);
            return vec![];
        };

        let crossed_above = pf <= ps && fv.fisher > fv.signal;
        let crossed_below = pf >= ps && fv.fisher < fv.signal;

        self.prev_fisher = Some(fv.fisher);
        self.prev_signal = Some(fv.signal);

        if crossed_above && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "fisher_crossover"
    }

    fn reset(&mut self) {
        self.fisher = Fisher::new(self.period);
        self.prev_fisher = None;
        self.prev_signal = None;
        self.in_position = false;
    }
}