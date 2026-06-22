use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Hma;

/// Bot — HMA Crossover (Hull Moving Average).
///
/// Long when fast HMA crosses above slow HMA.
/// Close when fast HMA crosses below slow HMA.
pub struct HmaCrossover {
    fast: Hma,
    slow: Hma,
    fast_period: usize,
    slow_period: usize,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
}

impl HmaCrossover {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        assert!(fast_period < slow_period, "fast_period must be < slow_period");
        Self {
            fast: Hma::new(fast_period),
            slow: Hma::new(slow_period),
            fast_period,
            slow_period,
            prev_fast: None,
            prev_slow: None,
        }
    }
}

impl Strategy for HmaCrossover {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let f = self.fast.update(bar.close);
        let s = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (f, s) else {
            return vec![];
        };

        let (Some(pf), Some(ps)) = (self.prev_fast, self.prev_slow) else {
            self.prev_fast = Some(f);
            self.prev_slow = Some(s);
            return vec![];
        };

        let crossed_above = pf <= ps && f > s;
        let crossed_below = pf >= ps && f < s;

        self.prev_fast = Some(f);
        self.prev_slow = Some(s);

        if crossed_above {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "hma_crossover"
    }

    fn reset(&mut self) {
        self.fast = Hma::new(self.fast_period);
        self.slow = Hma::new(self.slow_period);
        self.prev_fast = None;
        self.prev_slow = None;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let hma16 = ind.hma(16);
let hma49 = ind.hma(49);
if cross_above(hma16, hma49) { entry = true; }
if cross_below(hma16, hma49) { exit  = true; }
"#;
#[cfg(test)]
mod tests {
    use crate::test_utils::*;
    use super::*;
    use alm_core::signal::Direction;

    

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    

    #[test]
    fn no_signal_before_warmup() {
        let mut s = HmaCrossover::new(9, 21);
        let Some(bars) = load_real_bars() else { return; };
        for b in bars.iter().take(20) {
            assert!(s.on_bar(b).is_empty());
        }
    }


    #[test]
    fn produces_signals() {
        let Some(bars) = load_real_bars() else { return; };
        let mut hc = HmaCrossover::new(9, 21);
        let hc_sigs = run(&mut hc, &bars);

        assert!(!hc_sigs.is_empty(), "hma_crossover: no signals");
    }

    #[test]
    fn parity_reset() {
        let Some(bars) = load_real_bars() else { return; };
        let mut hc = HmaCrossover::new(9, 21);
        let r1 = run(&mut hc, &bars);
        hc.reset();
        let r2 = run(&mut hc, &bars);
        assert_eq!(r1, r2, "reset parity failed");
    }

    #[test]
    fn script_parity() {
        use crate::factory::build_strategy;
        use serde_json::json;
        

        let Some(bars) = load_real_bars() else { return; };

        let mut named = HmaCrossover::new(16, 49);
        let named_sigs = run(&mut named, &bars);

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "hma_crossover: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
