use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Cci;

/// Bot — CCI Reversal.
///
/// Long when CCI crosses above `entry_level` from below (default −100).
/// Close when CCI crosses above `exit_level` (default +100).
pub struct CciReversal {
    cci: Cci,
    entry_level: f64,
    exit_level: f64,
    prev_cci: Option<f64>,
    period: usize,
}

impl CciReversal {
    pub fn new(period: usize, entry_level: f64, exit_level: f64) -> Self {
        Self {
            cci: Cci::new(period),
            entry_level,
            exit_level,
            prev_cci: None,
            period,
        }
    }
}

impl Strategy for CciReversal {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.cci.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let prev = self.prev_cci.replace(v);
        let Some(p) = prev else {
            return vec![];
        };

        // Cross above entry_level (e.g. -100 → oversold recovery)
        if p <= self.entry_level && v > self.entry_level {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        // Cross above exit_level (e.g. +100 → overbought)
        if p <= self.exit_level && v > self.exit_level {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "cci_reversal"
    }

    fn reset(&mut self) {
        self.cci = Cci::new(self.period);
        self.prev_cci = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn script_parity() {
        let bars = trending_bars(300);

        let mut named = CciReversal::new(20, -100.0, 100.0);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let cci20 = ind.cci(20);
if cci20[1] <= -100.0 && cci20[0] > -100.0 { entry = true; }
if cci20[1] <= 100.0 && cci20[0] > 100.0 { exit = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "cci_reversal: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
