use std::collections::VecDeque;

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};

/// Bot — Highest High Breakout (Donchian-style).
///
/// Long when current close breaks above the highest close of the previous `period` bars.
/// Close when close drops below the lowest close of the previous `period` bars.
pub struct HighestBreakout {
    period: usize,
    closes: VecDeque<f64>,
}

impl HighestBreakout {
    pub fn new(period: usize) -> Self {
        assert!(period >= 2);
        Self {
            period,
            closes: VecDeque::with_capacity(period + 1),
        }
    }
}

impl Strategy for HighestBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        self.closes.push_back(bar.close);
        if self.closes.len() > self.period + 1 {
            self.closes.pop_front();
        }
        if self.closes.len() < self.period + 1 {
            return vec![];
        }

        // Lookback window = all bars except the current one
        let lookback = self.closes.range(..self.period);
        let highest = lookback.cloned().fold(f64::NEG_INFINITY, f64::max);
        let lowest = self.closes.range(..self.period).cloned().fold(f64::INFINITY, f64::min);

        if bar.close > highest {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < lowest {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "highest_breakout"
    }

    fn reset(&mut self) {
        self.closes.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = HighestBreakout::new(20);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
let dummy = highest(close, 21);
let highest_val = close[1];
let lowest_val = close[1];
let i = 2;
while i <= 20 {
    if close[i] > highest_val { highest_val = close[i]; }
    if close[i] < lowest_val { lowest_val = close[i]; }
    i = i + 1;
}
if close[0] > highest_val { entry = true; }
if close[0] < lowest_val { exit = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "highest_breakout: must produce signals");
        assert_parity("highest_breakout parity vs named", &named_sigs, &script_sigs);
    }
}
