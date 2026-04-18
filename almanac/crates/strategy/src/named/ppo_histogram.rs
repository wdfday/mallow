use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Ppo;

/// Percentage Price Oscillator histogram zero-cross.
///
/// PPO normalises MACD to percentage — comparable across symbols with different price scales.
///
/// Long when PPO histogram crosses above zero (momentum turning positive).
/// Close when PPO histogram crosses below zero.
pub struct PpoHistogram {
    ppo: Ppo,
    prev_hist: Option<f64>,
    in_position: bool,
    fast: usize,
    slow: usize,
    signal: usize,
}

impl PpoHistogram {
    pub fn new(fast: usize, slow: usize, signal: usize) -> Self {
        Self {
            ppo: Ppo::new(fast, slow, signal),
            prev_hist: None,
            in_position: false,
            fast,
            slow,
            signal,
        }
    }
}

impl Strategy for PpoHistogram {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(pv) = self.ppo.update(bar.close) else {
            return vec![];
        };

        let Some(prev) = self.prev_hist else {
            self.prev_hist = Some(pv.histogram);
            return vec![];
        };

        let crossed_above = prev <= 0.0 && pv.histogram > 0.0;
        let crossed_below = prev >= 0.0 && pv.histogram < 0.0;
        self.prev_hist = Some(pv.histogram);

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
        "ppo_histogram"
    }

    fn reset(&mut self) {
        self.ppo = Ppo::new(self.fast, self.slow, self.signal);
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

    #[test]
    fn ppo_histogram_parity() {
        let bars = trending_bars(300);

        let mut hc = PpoHistogram::new(12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "ppo": { "type": "ppo", "fast": 12, "slow": 26, "signal": 9 } },
            "entry": { "logic": "and", "rules": [
                { "source": "ppo", "field": "histogram", "op": "cross_above", "value": 0.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "ppo", "field": "histogram", "op": "cross_below", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ppo_hist(12) <= 0.0 && ppo_hist(12) > 0.0",
            "exit":  "prev_ppo_hist(12) >= 0.0 && ppo_hist(12) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "ppo_histogram: no signals");
        assert_parity("ppo_histogram hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("ppo_histogram hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
