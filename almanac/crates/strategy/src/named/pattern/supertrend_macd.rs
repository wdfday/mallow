use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Macd, SuperTrend};

pub(crate) const RHAI_SCRIPT: &str = r#"
let st10 = ind.supertrend(10, buf=1);
let m    = ind.macd(12, buf=1);
if st10[0].bullish >= 0.5 && m[0].histogram > 0.0 { entry = true; }
if st10[0].bullish < 0.5  { exit  = true; }
"#;

/// Bot — SuperTrend + MACD confirmation.
///
/// Long when SuperTrend is bullish AND MACD histogram > 0.
/// Close when SuperTrend flips bearish.
pub struct SupertrendMacd {
    st: SuperTrend,
    macd: Macd,
    st_period: usize,
    multiplier: f64,
    macd_fast: usize,
    macd_slow: usize,
    macd_signal: usize,
}

impl SupertrendMacd {
    pub fn new(
        st_period: usize,
        multiplier: f64,
        macd_fast: usize,
        macd_slow: usize,
        macd_signal: usize,
    ) -> Self {
        Self {
            st: SuperTrend::new(st_period, multiplier),
            macd: Macd::new(macd_fast, macd_slow, macd_signal),
            st_period,
            multiplier,
            macd_fast,
            macd_slow,
            macd_signal,
        }
    }
}

impl Strategy for SupertrendMacd {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let st = self.st.update(bar.high, bar.low, bar.close);
        let mc = self.macd.update(bar.close);

        let (Some(st), Some(mc)) = (st, mc) else {
            return vec![];
        };

        if st.is_bullish && mc.histogram > 0.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !st.is_bullish {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "supertrend_macd"
    }

    fn description(&self) -> &'static str {
        "Long when SuperTrend is bullish and MACD histogram is positive. Exit when SuperTrend flips bearish."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn reset(&mut self) {
        self.st = SuperTrend::new(self.st_period, self.multiplier);
        self.macd = Macd::new(self.macd_fast, self.macd_slow, self.macd_signal);
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
    fn supertrend_macd_script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = SupertrendMacd::new(10, 3.0, 12, 26, 9);
        let named_sigs = run(&mut named, &bars);

        let script = SupertrendMacd::new(10, 3.0, 12, 26, 9).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "supertrend_macd: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
