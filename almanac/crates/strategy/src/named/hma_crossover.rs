use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Hma;

/// Bot — HMA Crossover (Hull Moving Average).
///
/// Long when fast HMA crosses above slow HMA.
/// Close when fast HMA crosses below slow HMA.
pub struct HmaCrossover {
    fast: Hma,
    slow: Hma,
    fast_period: usize,
    slow_period: usize,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
}

impl HmaCrossover {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        assert!(fast_period < slow_period, "fast_period must be < slow_period");
        Self {
            fast: Hma::new(fast_period),
            slow: Hma::new(slow_period),
            fast_period,
            slow_period,
            prev_fast: None,
            prev_slow: None,
            in_position: false,
        }
    }
}

impl Strategy for HmaCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let f = self.fast.update(bar.close);
        let s = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (f, s) else {
            return vec![];
        };

        let (Some(pf), Some(ps)) = (self.prev_fast, self.prev_slow) else {
            self.prev_fast = Some(f);
            self.prev_slow = Some(s);
            return vec![];
        };

        let crossed_above = pf <= ps && f > s;
        let crossed_below = pf >= ps && f < s;

        self.prev_fast = Some(f);
        self.prev_slow = Some(s);

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
        "hma_crossover"
    }

    fn reset(&mut self) {
        self.fast = Hma::new(self.fast_period);
        self.slow = Hma::new(self.slow_period);
        self.prev_fast = None;
        self.prev_slow = None;
        self.in_position = false;
    }
}
