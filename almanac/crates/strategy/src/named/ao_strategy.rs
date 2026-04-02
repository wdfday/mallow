//! Awesome Oscillator Strategy
//!
//! Entry: AO crosses above zero (momentum turning bullish)
//! Exit: AO crosses below zero

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::AwesomeOscillator;

pub struct AoStrategy {
    ao: AwesomeOscillator,
    prev_ao: Option<f64>,
    in_position: bool,
}

impl AoStrategy {
    pub fn new(fast: usize, slow: usize) -> Self {
        Self {
            ao: AwesomeOscillator::new(fast, slow),
            prev_ao: None,
            in_position: false,
        }
    }
}

impl Strategy for AoStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(ao) = self.ao.update(bar.high, bar.low) else {
            return vec![];
        };

        let mut signals = vec![];

        if let Some(prev) = self.prev_ao {
            if !self.in_position && prev < 0.0 && ao >= 0.0 {
                self.in_position = true;
                signals.push(Signal::long(bar.timestamp, &bar.symbol, 1.0));
            } else if self.in_position && prev >= 0.0 && ao < 0.0 {
                self.in_position = false;
                signals.push(Signal::close(bar.timestamp, &bar.symbol));
            }
        }

        self.prev_ao = Some(ao);
        signals
    }

    fn name(&self) -> &str { "ao" }

    fn reset(&mut self) {
        self.ao.reset();
        self.prev_ao = None;
        self.in_position = false;
    }
}
