//! Awesome Oscillator Strategy
//!
//! Entry: AO crosses above zero (momentum turning bullish)
//! Exit: AO crosses below zero

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::AwesomeOscillator;

pub struct AoStrategy {
    ao: AwesomeOscillator,
    prev_ao: Option<f64>,
}

impl AoStrategy {
    pub fn new(fast: usize, slow: usize) -> Self {
        Self {
            ao: AwesomeOscillator::new(fast, slow),
            prev_ao: None,
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
            if  prev <= 0.0 && ao > 0.0 {
                signals.push(Signal::long(bar.timestamp, &bar.symbol, 1.0));
            } else if prev >= 0.0 && ao < 0.0 {
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
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn rhai_parity() {
        let bars = slow_trend_bars();

        let mut named = AoStrategy::new(5, 34);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let ao = ind.ao(0);
if ao[1] <= 0.0 && ao[0] > 0.0 { entry = true; }
if ao[1] >= 0.0 && ao[0] < 0.0 { exit = true; }
"#;
        let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
        let rhai_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| rhai.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "ao_strategy: must produce signals");
        assert_eq!(named_sigs, rhai_sigs, "rhai parity failed");
    }
}
