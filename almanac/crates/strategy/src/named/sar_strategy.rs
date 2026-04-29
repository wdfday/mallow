use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::ParabolicSar;

/// Bot — Parabolic SAR trend follower.
///
/// Long when SAR flips bullish (price crosses above SAR).
/// Close when SAR flips bearish.
pub struct SarStrategy {
    sar: ParabolicSar,
    step: f64,
    max: f64,
    prev_bullish: Option<bool>,
    in_position: bool,
}

impl SarStrategy {
    pub fn new(step: f64, max: f64) -> Self {
        Self {
            sar: ParabolicSar::new(step, max),
            step,
            max,
            prev_bullish: None,
            in_position: false,
        }
    }
}

impl Strategy for SarStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.sar.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let prev = self.prev_bullish.replace(v.is_bullish);
        let Some(was_bullish) = prev else {
            return vec![];
        };

        if v.is_bullish && !was_bullish && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.is_bullish && was_bullish && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "parabolic_sar"
    }

    fn reset(&mut self) {
        self.sar = ParabolicSar::new(self.step, self.max);
        self.prev_bullish = None;
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
    fn sar_parity() {
        let bars = sar_bars();

        let mut hc = SarStrategy::new(0.02, 0.2);
        let hc_sigs = run(&mut hc, &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(1) < prev_sar(1) && ema(1) > sar(1)",
            "exit":  "prev_ema(1) > prev_sar(1) && ema(1) < sar(1)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "sar: no signals");
        assert_parity("sar hc vs cel", &hc_sigs, &cel_sigs);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "sar": { "type": "parabolic_sar", "step": 0.02, "max": 0.2 } },
            "entry": { "logic": "and", "rules": [
                { "source": "sar", "field": "bullish", "op": "cross_above", "value": 0.5 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "sar", "field": "bullish", "op": "cross_below", "value": 0.5 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);
        assert!(dyn_sigs.len() >= hc_sigs.len(), "sar dynamic should have at least as many signals as hc");
        let dyn_tail: Vec<_> = dyn_sigs.iter().skip(dyn_sigs.len() - hc_sigs.len()).cloned().collect();
        assert_parity("sar hc matches tail of dynamic", &hc_sigs, &dyn_tail);
    }
    */
}
