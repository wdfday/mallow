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
    in_position: bool,
    period: usize,
}

impl CciReversal {
    pub fn new(period: usize, entry_level: f64, exit_level: f64) -> Self {
        Self {
            cci: Cci::new(period),
            entry_level,
            exit_level,
            prev_cci: None,
            in_position: false,
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
        if p <= self.entry_level && v > self.entry_level && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        // Cross above exit_level (e.g. +100 → overbought)
        if p <= self.exit_level && v > self.exit_level && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "cci_reversal"
    }

    fn reset(&mut self) {
        self.cci = Cci::new(self.period);
        self.prev_cci = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn cci_reversal_parity() {
        let bars = rsi_bars(80); // strong V-shape pushes CCI through ±100

        // 1. hardcoded (entry cross_above -100, exit cross_above +100)
        let mut hc = CciReversal::new(14, -100.0, 100.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "cci": { "type": "cci", "period": 14 } },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "cci", "field": "value", "op": "cross_above", "value": -100.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "cci", "field": "value", "op": "cross_above", "value": 100.0 }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_cci(14) <= -100.0 && cci(14) > -100.0",
            "exit":  "prev_cci(14) <= 100.0 && cci(14) > 100.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "cci_reversal: hardcoded produced no signals");
        assert_parity("cci hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("cci hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }
}
