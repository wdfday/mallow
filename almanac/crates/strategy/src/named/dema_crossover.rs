use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Dema;

/// DEMA Crossover — faster signal generation than EMA crossover due to reduced lag.
///
/// Long  when fast DEMA crosses above slow DEMA.
/// Close when fast DEMA crosses below slow DEMA.
pub struct DemaCrossover {
    fast: Dema,
    slow: Dema,
    fast_p: usize,
    slow_p: usize,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
}

impl DemaCrossover {
    pub fn new(fast: usize, slow: usize) -> Self {
        Self {
            fast: Dema::new(fast),
            slow: Dema::new(slow),
            fast_p: fast,
            slow_p: slow,
            prev_fast: None,
            prev_slow: None,
            in_position: false,
        }
    }
}

impl Strategy for DemaCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let f = self.fast.update(bar.close);
        let s = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (f, s) else {
            return vec![];
        };

        let prev_f = self.prev_fast.replace(f);
        let prev_s = self.prev_slow.replace(s);

        let (Some(pf), Some(ps)) = (prev_f, prev_s) else {
            return vec![];
        };

        if pf <= ps && f > s && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if pf >= ps && f < s && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "dema_crossover"
    }

    fn reset(&mut self) {
        self.fast = Dema::new(self.fast_p);
        self.slow = Dema::new(self.slow_p);
        self.prev_fast = None;
        self.prev_slow = None;
        self.in_position = false;
    }
}
