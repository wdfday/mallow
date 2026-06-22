use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Dema;

/// DEMA Crossover — faster signal generation than EMA crossover due to reduced lag.
///
/// Long  when fast DEMA crosses above slow DEMA.
/// Close when fast DEMA crosses below slow DEMA.
pub struct DemaCrossover {
    fast: Dema,
    slow: Dema,
    fast_p: usize,
    slow_p: usize,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
}

impl DemaCrossover {
    pub fn new(fast: usize, slow: usize) -> Self {
        Self {
            fast: Dema::new(fast),
            slow: Dema::new(slow),
            fast_p: fast,
            slow_p: slow,
            prev_fast: None,
            prev_slow: None,
        }
    }
}

impl Strategy for DemaCrossover {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let f = self.fast.update(bar.close);
        let s = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (f, s) else {
            return vec![];
        };

        let prev_f = self.prev_fast.replace(f);
        let prev_s = self.prev_slow.replace(s);

        let (Some(pf), Some(ps)) = (prev_f, prev_s) else {
            return vec![];
        };

        if pf <= ps && f > s {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if pf >= ps && f < s {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "dema_crossover"
    }

    fn reset(&mut self) {
        self.fast = Dema::new(self.fast_p);
        self.slow = Dema::new(self.slow_p);
        self.prev_fast = None;
        self.prev_slow = None;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let dema12 = ind.dema(12);
let dema26 = ind.dema(26);
if cross_above(dema12, dema26) { entry = true; }
if cross_below(dema12, dema26) { exit  = true; }
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
        let mut s = DemaCrossover::new(10, 25);
        let Some(bars) = load_real_bars() else { return; };
        for b in bars.iter().take(40) {
            assert!(s.on_bar(b).is_empty());
        }
    }
    
    #[test]
    fn produces_signals() {
        let Some(bars) = load_real_bars() else { return; };
        let mut hc = DemaCrossover::new(10, 25);
        let hc_sigs = run(&mut hc, &bars);

        assert!(!hc_sigs.is_empty(), "dema_crossover: no signals");
    }

    #[test]
    fn parity_reset() {
        let Some(bars) = load_real_bars() else { return; };
        let mut hc = DemaCrossover::new(10, 25);
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

        let mut named = DemaCrossover::new(12, 26);
        let named_sigs = run(&mut named, &bars);

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "dema_crossover: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
