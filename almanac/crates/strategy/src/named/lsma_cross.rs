use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Lsma;

/// Least Squares MA fast/slow crossover.
///
/// LSMA fits a linear regression to the last N bars and returns the endpoint,
/// giving lower lag than EMA while remaining smooth.
///
/// Long when fast LSMA crosses above slow LSMA.
/// Close when fast LSMA crosses below slow LSMA.
pub struct LsmaCross {
    fast: Lsma,
    slow: Lsma,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
    fast_period: usize,
    slow_period: usize,
}

impl LsmaCross {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        assert!(fast_period < slow_period, "fast must be < slow");
        Self {
            fast: Lsma::new(fast_period),
            slow: Lsma::new(slow_period),
            prev_fast: None,
            prev_slow: None,
            in_position: false,
            fast_period,
            slow_period,
        }
    }
}

impl Strategy for LsmaCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (fast, slow) else {
            return vec![];
        };

        let fv = f.value;
        let sv = s.value;

        let (Some(pf), Some(ps)) = (self.prev_fast, self.prev_slow) else {
            self.prev_fast = Some(fv);
            self.prev_slow = Some(sv);
            return vec![];
        };

        let crossed_above = pf <= ps && fv > sv;
        let crossed_below = pf >= ps && fv < sv;
        self.prev_fast = Some(fv);
        self.prev_slow = Some(sv);

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
        "lsma_cross"
    }

    fn reset(&mut self) {
        self.fast = Lsma::new(self.fast_period);
        self.slow = Lsma::new(self.slow_period);
        self.prev_fast = None;
        self.prev_slow = None;
        self.in_position = false;
    }
}
