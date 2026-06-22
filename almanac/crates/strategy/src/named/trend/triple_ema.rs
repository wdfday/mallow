use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Ema;

/// Bot #35 — Triple EMA Crossover.
///
/// Long when all three EMAs are aligned bullishly: EMA(fast) > EMA(mid) > EMA(slow).
/// Entry on the first bar where the alignment holds (transition from non-bull to bull).
/// Exit when any EMA inverts the alignment.
pub struct TripleEma {
    ema1: Ema,
    ema2: Ema,
    ema3: Ema,
    prev_bull: Option<bool>,
    period1: usize,
    period2: usize,
    period3: usize,
}

impl TripleEma {
    pub fn new(period1: usize, period2: usize, period3: usize) -> Self {
        assert!(period1 < period2 && period2 < period3, "periods must be ascending");
        Self {
            ema1: Ema::new(period1),
            ema2: Ema::new(period2),
            ema3: Ema::new(period3),
            prev_bull: None,
            period1,
            period2,
            period3,
        }
    }
}

impl Strategy for TripleEma {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let e1 = self.ema1.update(bar.close);
        let e2 = self.ema2.update(bar.close);
        let e3 = self.ema3.update(bar.close);

        let (Some(e1), Some(e2), Some(e3)) = (e1, e2, e3) else {
            return vec![];
        };

        let bull = e1 > e2 && e2 > e3;
        let prev = self.prev_bull.replace(bull);

        let Some(was_bull) = prev else {
            return vec![];
        };

        if !was_bull && bull {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if was_bull && !bull {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "triple_ema"
    }

    fn reset(&mut self) {
        self.ema1 = Ema::new(self.period1);
        self.ema2 = Ema::new(self.period2);
        self.ema3 = Ema::new(self.period3);
        self.prev_bull = None;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let e10 = ind.ema(10);
let e20 = ind.ema(20);
let e50 = ind.ema(50);
let bull_now  = e10[0] > e20[0] && e20[0] > e50[0];
let bull_prev = e10[1] > e20[1] && e20[1] > e50[1];
if !bull_prev && bull_now  { entry = true; }
if bull_prev  && !bull_now { exit  = true; }
"#;
#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = TripleEma::new(10, 20, 50);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "triple_ema: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
