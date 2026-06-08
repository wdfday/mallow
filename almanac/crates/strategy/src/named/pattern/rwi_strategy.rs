use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Rwi;

const RHAI: &str = r#"
let rwi14 = ind.rwi(14, buf=1);
if rwi14[0].rwi_high > 1.0 { entry = true; }
if rwi14[0].rwi_low  > 1.0 { exit  = true; }
"#;

/// Bot — Random Walk Index (Schwager).
///
/// Long when RWI_High > `threshold` (non-random uptrend detected).
/// Close when RWI_Low > `threshold` (non-random downtrend).
pub struct RwiStrategy {
    rwi: Rwi,
    threshold: f64,
    period: usize,
}

impl RwiStrategy {
    pub fn new(period: usize, threshold: f64) -> Self {
        Self {
            rwi: Rwi::new(period),
            threshold,
            period,
        }
    }
}

impl Strategy for RwiStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.rwi.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if v.rwi_high > self.threshold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, (v.rwi_high - 1.0).min(1.0).max(0.0))];
        }
        if v.rwi_low > self.threshold {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "rwi"
    }

    fn description(&self) -> &'static str {
        "Long when RWI High > threshold (non-random uptrend). Exit when RWI Low > threshold (non-random downtrend)."
    }

    fn script(&self) -> Option<&'static str> { Some(RHAI) }

    fn reset(&mut self) {
        self.rwi = Rwi::new(self.period);
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
        // RWI fires every bar where rwi_high > 1 (entry) or rwi_low > 1 (exit).
        let Some(bars) = load_real_bars() else { return; };

        let mut named = RwiStrategy::new(14, 1.0);
        let named_sigs = run(&mut named, &bars);

        let script = RwiStrategy::new(14, 1.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "rwi: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
