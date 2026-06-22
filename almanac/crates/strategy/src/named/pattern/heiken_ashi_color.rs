use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::HeikenAshi;

pub(crate) const RHAI_SCRIPT: &str = r#"
candle.transform("heiken_ashi");
let is_bullish = close[0] >= open[0];
let was_bullish = close[1] >= open[1];
if is_bullish && !was_bullish { entry = true; }
if !is_bullish && was_bullish { exit = true; }
"#;

/// Bot — Heiken Ashi Trend Color.
///
/// Long when HA candle flips to bullish (red → green).
/// Close when HA candle flips to bearish.
pub struct HaColor {
    ha: HeikenAshi,
    smooth: usize,
    prev_bullish: Option<bool>,
}

impl HaColor {
    /// `smooth = 1` = standard HA, no EMA smoothing.
    pub fn new(smooth: usize) -> Self {
        Self {
            ha: HeikenAshi::new(smooth),
            smooth,
            prev_bullish: None,
        }
    }
}

impl Strategy for HaColor {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.ha.update(bar.open, bar.high, bar.low, bar.close) else {
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
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "heiken_ashi_color"
    }

    fn reset(&mut self) {
        self.ha = HeikenAshi::new(self.smooth);
        self.prev_bullish = None;
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
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
    fn script_parity_ha_color() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = HaColor::new(1);
        let named_sigs = run(&mut named, &bars);

        let script = HaColor::new(1).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "heiken_ashi_color: must produce signals");
        assert_parity("heiken_ashi_color parity vs named", &named_sigs, &script_sigs);
    }
}
