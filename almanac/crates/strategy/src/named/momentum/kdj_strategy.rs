//! KDJ Strategy
//!
//! Long when K and D both cross below oversold and J turns up.
//! Exit when K crosses above overbought or J exceeds 100.

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Kdj;

pub struct KdjStrategy {
    kdj: Kdj,
    oversold: f64,
    overbought: f64,
    prev_k: Option<f64>,
    in_position: bool,
}

impl KdjStrategy {
    pub fn new(period: usize, k_period: usize, d_period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            kdj: Kdj::new(period, k_period, d_period),
            oversold,
            overbought,
            prev_k: None,
            in_position: false,
        }
    }
}

impl Strategy for KdjStrategy {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.kdj.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let mut signals = vec![];

        if !self.in_position {
            // Entry: K and D both below oversold, J starting to turn up
            if let Some(pk) = self.prev_k {
                if v.k < self.oversold && v.d < self.oversold && v.k > pk {
                    self.in_position = true;
                    signals.push(Signal::long(bar.timestamp, &bar.symbol, 1.0));
                }
            }
        } else {
            // Exit: K above overbought or J exceeds 100 (exhaustion)
            if v.k > self.overbought || v.j > 100.0 {
                self.in_position = false;
                signals.push(Signal::exit(bar.timestamp, &bar.symbol));
            }
        }

        self.prev_k = Some(v.k);
        signals
    }

    fn name(&self) -> &str { "kdj" }

    fn reset(&mut self) {
        self.kdj.reset();
        self.prev_k = None;
        self.in_position = false;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let kdj9 = ind.kdj(9);
if state["in_position"] == () {
    state["in_position"] = false;
}
let in_pos = state["in_position"];
if !in_pos {
    if kdj9[0].k < 20.0 && kdj9[0].d < 20.0 && gt(kdj9[0].k, kdj9[1].k) {
        state["in_position"] = true;
        entry = true;
    }
} else {
    if kdj9[0].k > 80.0 || kdj9[0].j > 100.0 {
        state["in_position"] = false;
        exit = true;
    }
}
"#;
#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;
    use crate::test_utils::*;

    #[test]
    fn kdj_hc_produces_signals() {
        let Some(bars) = load_real_bars() else { return; };

        let mut hc = KdjStrategy::new(9, 3, 3, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);
        assert!(!hc_sigs.is_empty(), "kdj: no signals");
    }

    #[test]
    fn script_parity() {
        use crate::factory::build_strategy;
        use serde_json::json;

        let Some(bars) = load_real_bars() else { return; };

        let mut named = KdjStrategy::new(9, 3, 3, 20.0, 80.0);
        let named_sigs = run(&mut named, &bars);
        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();

        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "kdj: must produce signals");
        assert_parity("kdj parity vs named", &named_sigs, &script_sigs);
    }
}
