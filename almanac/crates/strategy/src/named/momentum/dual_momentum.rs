use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Roc;

pub(crate) const RHAI_SCRIPT: &str = r#"
let roc10 = ind.roc(10, buf=1);
let roc30 = ind.roc(30, buf=1);
if roc10[0] > 0.0 && roc30[0] > 0.0 { entry = true; }
if roc10[0] < 0.0 || roc30[0] < 0.0 { exit  = true; }
"#;

/// Dual Momentum — absolute + relative momentum filter.
///
/// Long  when ROC(fast) > 0 AND ROC(slow) > 0 (both timeframes agree).
/// Close when either turns negative.
pub struct DualMomentum {
    fast: Roc,
    slow: Roc,
    fast_p: usize,
    slow_p: usize,
}

impl DualMomentum {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        Self {
            fast: Roc::new(fast_period),
            slow: Roc::new(slow_period),
            fast_p: fast_period,
            slow_p: slow_period,
        }
    }
}

impl Strategy for DualMomentum {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let f = self.fast.update(bar.close);
        let s = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (f, s) else {
            return vec![];
        };

        if f > 0.0 && s > 0.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if f < 0.0 || s < 0.0 {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "dual_momentum"
    }

    fn description(&self) -> &'static str {
        "Long when both fast and slow ROC are positive. Exit when either turns negative."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn reset(&mut self) {
        self.fast = Roc::new(self.fast_p);
        self.slow = Roc::new(self.slow_p);
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
    fn dual_momentum_script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = DualMomentum::new(10, 30);
        let named_sigs = run(&mut named, &bars);

        let script = DualMomentum::new(10, 30).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "dual_momentum: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
