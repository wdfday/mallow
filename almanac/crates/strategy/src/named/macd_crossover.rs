use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Macd;

/// Bot #2 — MACD Crossover.
///
/// Long when MACD histogram crosses above zero (MACD line crosses above signal).
/// Closes when histogram crosses below zero.
pub struct MacdCrossover {
    macd: Macd,
    prev_hist: Option<f64>,
    in_position: bool,
    fast: usize,
    slow: usize,
    signal_period: usize,
}

impl MacdCrossover {
    pub fn new(fast: usize, slow: usize, signal_period: usize) -> Self {
        Self {
            macd: Macd::new(fast, slow, signal_period),
            prev_hist: None,
            in_position: false,
            fast,
            slow,
            signal_period,
        }
    }
}

impl Strategy for MacdCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.macd.update(bar.close) else {
            return vec![];
        };

        let Some(prev) = self.prev_hist else {
            self.prev_hist = Some(v.histogram);
            return vec![];
        };

        let crossed_up = prev <= 0.0 && v.histogram > 0.0;
        let crossed_down = prev >= 0.0 && v.histogram < 0.0;
        self.prev_hist = Some(v.histogram);

        if crossed_up && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_down && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "macd_crossover"
    }

    fn reset(&mut self) {
        self.macd = Macd::new(self.fast, self.slow, self.signal_period);
        self.prev_hist = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    #[test]
    fn rhai_parity() {
        let bars = slow_trend_bars();

        let mut named = MacdCrossover::new(12, 26, 9);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
let mh = ind.macd_hist(12);
if mh[1] <= 0.0 && mh[0] > 0.0 { entry = true; }
if mh[1] >= 0.0 && mh[0] < 0.0 { exit  = true; }
"#;
        let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
        let rhai_sigs = run(rhai.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "macd_crossover: must produce signals");
        assert_eq!(named_sigs, rhai_sigs, "rhai parity failed");
    }
}
