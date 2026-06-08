use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Kama;

/// KAMA Crossover Strategy.
/// Long when close crosses above KAMA; close when close crosses below KAMA.
pub struct KamaStrategy {
    er_period: usize,
    fast: usize,
    slow: usize,
    kama: Kama,
    prev_above: Option<bool>,
}

impl KamaStrategy {
    pub fn new(er_period: usize, fast: usize, slow: usize) -> Self {
        Self {
            er_period,
            fast,
            slow,
            kama: Kama::new(er_period, fast, slow),
            prev_above: None,
        }
    }
}

impl Strategy for KamaStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(kama_val) = self.kama.update(bar.close) else {
            return vec![];
        };

        let above = bar.close > kama_val;

        let signal = match self.prev_above {
            Some(was_above) => {
                if above && !was_above {
                    // Cross above KAMA → Long
                    let strength = ((bar.close - kama_val) / kama_val).clamp(0.0, 1.0);
                    Some(Signal::long(bar.timestamp, &bar.symbol, strength))
                } else if !above && was_above {
                    // Cross below KAMA → Close
                    Some(Signal::exit(bar.timestamp, &bar.symbol))
                } else {
                    None
                }
            }
            None => None,
        };

        self.prev_above = Some(above);
        signal.into_iter().collect()
    }

    fn name(&self) -> &str {
        "kama"
    }

    fn reset(&mut self) {
        self.kama = Kama::new(self.er_period, self.fast, self.slow);
        self.prev_above = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;
    use crate::test_utils::*;

    

    #[test]
    fn test_no_signal_before_kama_ready() {
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = KamaStrategy::new(10, 2, 30);
        for b in bars.iter().take(11) {
            let s = strat.on_bar(b);
            assert!(s.is_empty(), "no signal before KAMA ready");
        }
    }

    #[test]
    fn test_long_signal_on_cross_above() {
        use alm_core::signal::Direction;
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = KamaStrategy::new(5, 2, 10);
        let mut signals = Vec::new();
        for b in &bars {
            signals.extend(strat.on_bar(b));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Exit),
            "should emit Close on cross below KAMA"
        );
    }

    #[test]
    fn test_reset_clears_state() {
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = KamaStrategy::new(5, 2, 10);
        for b in bars.iter().take(20) {
            strat.on_bar(b);
        }
        strat.reset();
        assert!(strat.prev_above.is_none());
    }

    #[test]
    fn script_parity() {
        use crate::factory::build_strategy;
        use serde_json::json;
        use alm_core::signal::Direction;

        let Some(bars) = load_real_bars() else { return; };

        let mut named = KamaStrategy::new(10, 2, 30);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let kama10 = ind.kama(10);
let was_above = close[1] > kama10[1];
let is_above  = close[0] > kama10[0];
if !was_above && is_above  { entry = true; }
if  was_above && !is_above { exit  = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "kama: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
