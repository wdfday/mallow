use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Ppo;

const RHAI: &str = r#"
let ppo12 = ind.ppo(12);
if ppo12[1].histogram <= 0.0 && ppo12[0].histogram > 0.0 { entry = true; }
if ppo12[1].histogram >= 0.0 && ppo12[0].histogram < 0.0 { exit  = true; }
"#;

/// Percentage Price Oscillator histogram zero-cross.
///
/// PPO normalises MACD to percentage — comparable across symbols with different price scales.
///
/// Long when PPO histogram crosses above zero (momentum turning positive).
/// Close when PPO histogram crosses below zero.
pub struct PpoHistogram {
    ppo: Ppo,
    prev_hist: Option<f64>,
    fast: usize,
    slow: usize,
    signal: usize,
}

impl PpoHistogram {
    pub fn new(fast: usize, slow: usize, signal: usize) -> Self {
        Self {
            ppo: Ppo::new(fast, slow, signal),
            prev_hist: None,
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

        if crossed_above {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "ppo_histogram"
    }

    fn description(&self) -> &'static str {
        "Long when PPO histogram crosses above zero (momentum turning positive). Exit when it crosses below zero."
    }

    fn script(&self) -> Option<&'static str> { Some(RHAI) }

    fn reset(&mut self) {
        self.ppo = Ppo::new(self.fast, self.slow, self.signal);
        self.prev_hist = None;
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

        let mut named = PpoHistogram::new(12, 26, 9);
        let named_sigs = run(&mut named, &bars);

        // PPO histogram zero-cross — same logic as MACD histogram
        let script = PpoHistogram::new(12, 26, 9).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "ppo_histogram: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
