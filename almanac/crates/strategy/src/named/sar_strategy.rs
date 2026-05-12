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
}

impl SarStrategy {
    pub fn new(step: f64, max: f64) -> Self {
        Self {
            sar: ParabolicSar::new(step, max),
            step,
            max,
            prev_bullish: None,
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

        if v.is_bullish && !was_bullish {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.is_bullish && was_bullish {
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
        let bars = sar_bars();

        let mut named = SarStrategy::new(0.02, 0.2);
        let named_sigs = run(&mut named, &bars);

        // Detect SAR flip via close crossing the SAR value (is_bullish = close > sar)
        let script = r#"
let ps = ind.parabolic_sar(0);
let was_bull = ps[1].bullish >= 0.5;
let now_bull = ps[0].bullish >= 0.5;
if !was_bull && now_bull  { entry = true; }
if  was_bull && !now_bull { exit  = true; }
"#;
        let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
        let rhai_sigs = run(rhai.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "sar: must produce signals");
        assert_eq!(named_sigs, rhai_sigs, "rhai parity failed");
    }
}
