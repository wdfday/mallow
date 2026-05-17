use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Alligator;

const RHAI: &str = r#"
let al = ind.alligator(13);
if al[1].bullish < 0.5 && al[0].bullish >= 0.5 { entry = true; }
if al[1].bullish >= 0.5 && al[0].bullish < 0.5 { exit  = true; }
"#;

/// Williams Alligator.
///
/// Long when Alligator is bullish: Lips > Teeth > Jaw (alligator eating upward).
/// Close when alignment breaks (any line inverts).
pub struct AlligatorStrategy {
    alligator: Alligator,
    prev_bullish: Option<bool>,
}

impl AlligatorStrategy {
    pub fn new(jaw: usize, teeth: usize, lips: usize) -> Self {
        Self {
            alligator: Alligator::new(jaw, teeth, lips),
            prev_bullish: None,
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

        if v.bullish && !was_bullish {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.bullish && was_bullish {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "alligator"
    }

    fn description(&self) -> &'static str {
        "Long when Alligator is bullish: Lips > Teeth > Jaw. Exit when alignment breaks."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI)
    }

    fn reset(&mut self) {
        self.alligator = Alligator::default();
        self.prev_bullish = None;
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
    fn script_parity() {
        // AlligatorStrategy fires on bullish-flag transitions; use default jaw=13 teeth=8 lips=5.
        let bars = trending_bars(400);

        let mut named = AlligatorStrategy::new(13, 8, 5);
        let named_sigs = run(&mut named, &bars);

        // alligator Multi: .bullish is 1.0 when lips > teeth > jaw
        let script = AlligatorStrategy::new(13, 8, 5).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "alligator: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
