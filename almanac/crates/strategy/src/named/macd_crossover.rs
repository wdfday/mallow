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
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn macd_crossover_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (fast=12, slow=26, signal=9)
        let mut hc = MacdCrossover::new(12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — histogram cross_above/below 0
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "macd": { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "macd", "field": "histogram", "op": "cross_above", "value": 0.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "macd", "field": "histogram", "op": "cross_below", "value": 0.0 }]
            }
        }))
        .unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL with prev_
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_macd_hist(12) <= 0.0 && macd_hist(12) > 0.0",
            "exit":  "prev_macd_hist(12) >= 0.0 && macd_hist(12) < 0.0"
        }))
        .unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "macd: hardcoded produced no signals");
        assert_parity("macd hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("macd hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
