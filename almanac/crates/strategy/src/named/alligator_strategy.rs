use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Alligator;

/// Bot — Williams Alligator.
///
/// Long when Alligator is bullish: Lips > Teeth > Jaw (alligator eating upward).
/// Close when alignment breaks (any line inverts).
pub struct AlligatorStrategy {
    alligator: Alligator,
    prev_bullish: Option<bool>,
    in_position: bool,
}

impl AlligatorStrategy {
    pub fn new(jaw: usize, teeth: usize, lips: usize) -> Self {
        Self {
            alligator: Alligator::new(jaw, teeth, lips),
            prev_bullish: None,
            in_position: false,
        }
    }
}

impl Strategy for AlligatorStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.alligator.update(bar.high, bar.low) else {
            return vec![];
        };

        let prev = self.prev_bullish.replace(v.bullish);
        let Some(was_bullish) = prev else {
            return vec![];
        };

        if v.bullish && !was_bullish && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.bullish && was_bullish && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "alligator"
    }

    fn reset(&mut self) {
        self.alligator = Alligator::default();
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

    #[test]
    fn alligator_parity() {
        let bars = trending_bars(300);

        let mut hc = AlligatorStrategy::new(13, 8, 5);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "al": { "type": "alligator", "jaw": 13, "teeth": 8, "lips": 5 } },
            "entry": { "logic": "and", "rules": [
                { "source": "al", "field": "bullish", "op": "cross_above", "value": 0.5 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "al", "field": "bullish", "op": "cross_below", "value": 0.5 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_alligator_bull(13) < 1.0 && alligator_bull(13) >= 1.0",
            "exit":  "prev_alligator_bull(13) >= 1.0 && alligator_bull(13) < 1.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "alligator: no signals");
        assert_parity("alligator hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("alligator hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
