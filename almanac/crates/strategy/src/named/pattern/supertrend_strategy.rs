use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::SuperTrend;

pub(crate) const RHAI_SCRIPT: &str = r#"
let st = ind.supertrend(10);
if st[1].bullish < 0.5 && st[0].bullish >= 0.5 { entry = true; }
if st[1].bullish >= 0.5 && st[0].bullish < 0.5 { exit  = true; }
"#;

/// Bot — SuperTrend Trend Follower.
///
/// Long when SuperTrend is bullish (price above the band).
/// Close when SuperTrend flips bearish.
pub struct SupertrendStrategy {
    st: SuperTrend,
    period: usize,
    multiplier: f64,
    prev_bullish: Option<bool>,
}

impl SupertrendStrategy {
    pub fn new(period: usize, multiplier: f64) -> Self {
        Self {
            st: SuperTrend::new(period, multiplier),
            period,
            multiplier,
            prev_bullish: None,
        }
    }
}

impl Strategy for SupertrendStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.st.update(bar.high, bar.low, bar.close) else {
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
        "supertrend"
    }

    fn description(&self) -> &'static str {
        "Long when SuperTrend flips bullish (price above band). Exit when SuperTrend flips bearish."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn reset(&mut self) {
        self.st = SuperTrend::new(self.period, self.multiplier);
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
        let Some(bars) = load_real_bars() else { return; };

        let mut named = SupertrendStrategy::new(10, 3.0);
        let named_sigs = run(&mut named, &bars);

        let script = SupertrendStrategy::new(10, 3.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "supertrend: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
