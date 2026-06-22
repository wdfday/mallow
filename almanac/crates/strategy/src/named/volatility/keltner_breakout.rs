use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Keltner;

pub(crate) const RHAI_SCRIPT: &str = r#"
let kc20 = ind.keltner(20, buf=1);
if close[0] > kc20[0].upper { entry = true; }
if close[0] < kc20[0].lower { exit  = true; }
"#;

/// Bot — Keltner Channel Breakout.
///
/// Long when close breaks above upper Keltner band.
/// Exit when close drops below the lower Keltner band.
pub struct KeltnerBreakout {
    keltner: Keltner,
    period: usize,
    atr_period: usize,
    multiplier: f64,
}

impl KeltnerBreakout {
    pub fn new(period: usize, atr_period: usize, multiplier: f64) -> Self {
        Self {
            keltner: Keltner::new(period, atr_period, multiplier),
            period,
            atr_period,
            multiplier,
        }
    }
}

impl Strategy for KeltnerBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(kc) = self.keltner.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if bar.close > kc.upper {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < kc.lower {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "keltner_breakout"
    }

    fn description(&self) -> &'static str {
        "Long when close breaks above upper Keltner band. Exit when close drops below the lower Keltner band."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn reset(&mut self) {
        self.keltner = Keltner::new(self.period, self.atr_period, self.multiplier);
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
    fn keltner_breakout_script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = KeltnerBreakout::new(20, 10, 2.0);
        let named_sigs = run(&mut named, &bars);

        let script = KeltnerBreakout::new(20, 10, 2.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "keltner_breakout: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
