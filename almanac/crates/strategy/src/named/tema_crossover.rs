//! TEMA Crossover Strategy
//!
//! Long when fast TEMA crosses above slow TEMA.
//! Exit when fast TEMA crosses below slow TEMA.
//!
//! TEMA has less lag than EMA, so it reacts faster to trend changes.

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Tema;

pub struct TemaCrossover {
    fast: Tema,
    slow: Tema,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
}

impl TemaCrossover {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        Self {
            fast: Tema::new(fast_period),
            slow: Tema::new(slow_period),
            prev_fast: None,
            prev_slow: None,
            in_position: false,
        }
    }
}

impl Strategy for TemaCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (fast, slow) else {
            return vec![];
        };

        let mut signals = vec![];

        if let (Some(pf), Some(ps)) = (self.prev_fast, self.prev_slow) {
            let was_above = pf > ps;
            let now_above = f > s;

            if !self.in_position && !was_above && now_above {
                self.in_position = true;
                signals.push(Signal::long(bar.timestamp, &bar.symbol, 1.0));
            } else if self.in_position && was_above && !now_above {
                self.in_position = false;
                signals.push(Signal::close(bar.timestamp, &bar.symbol));
            }
        }

        self.prev_fast = Some(f);
        self.prev_slow = Some(s);
        signals
    }

    fn name(&self) -> &str { "tema_crossover" }

    fn reset(&mut self) {
        self.fast.reset();
        self.slow.reset();
        self.prev_fast = None;
        self.prev_slow = None;
        self.in_position = false;
    }
}
