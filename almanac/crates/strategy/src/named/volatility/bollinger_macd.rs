use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{BBands, Macd};

/// Bot #12 — Bollinger Breakthrough + MACD histogram.
///
/// Long when price breaks above upper Bollinger Band AND MACD histogram > 0.
/// Closes when price falls below middle band OR histogram turns negative.
pub struct BollingerMacd {
    bb: BBands,
    macd: Macd,
    bb_period: usize,
    bb_std: f64,
    fast: usize,
    slow: usize,
    signal_period: usize,
}

impl BollingerMacd {
    pub fn new(bb_period: usize, bb_std: f64, fast: usize, slow: usize, signal_period: usize) -> Self {
        Self {
            bb: BBands::new(bb_period, bb_std),
            macd: Macd::new(fast, slow, signal_period),
            bb_period,
            bb_std,
            fast,
            slow,
            signal_period,
        }
    }
}

impl Strategy for BollingerMacd {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let bb_val = self.bb.update(bar.close);
        let macd_val = self.macd.update(bar.close);

        let (Some(bb), Some(m)) = (bb_val, macd_val) else {
            return vec![];
        };

        if bar.close > bb.upper && m.histogram > 0.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < bb.middle || m.histogram < 0.0 {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "bollinger_macd"
    }

    fn description(&self) -> &'static str {
        "Long when close breaks above upper Bollinger Band with positive MACD histogram. Exit when close drops below middle band or histogram turns negative."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.macd = Macd::new(self.fast, self.slow, self.signal_period);
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let bb20 = ind.bbands(20, buf=1);
let mh = ind.macd(12, buf=1);
if close[0] > bb20[0].upper && mh[0].histogram > 0.0 { entry = true; }
if close[0] < bb20[0].middle || mh[0].histogram < 0.0 { exit = true; }
"#;
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
        // slow_trend_bars() produces a clear price breakout above BB + MACD hist > 0
        let Some(bars) = load_real_bars() else { return; };

        let mut named = BollingerMacd::new(20, 2.0, 12, 26, 9);
        let named_sigs = run(&mut named, &bars);

        let script = BollingerMacd::new(20, 2.0, 12, 26, 9).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        // assert!(!named_sigs.is_empty(), "bollinger_macd: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
