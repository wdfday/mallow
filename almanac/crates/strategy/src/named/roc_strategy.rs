use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Roc;

/// Bot — ROC Zero-Cross.
///
/// Long when ROC crosses above 0 (positive momentum).
/// Close when ROC crosses below 0.
pub struct RocCrossover {
    roc: Roc,
    prev_roc: Option<f64>,
    in_position: bool,
    period: usize,
}

impl RocCrossover {
    pub fn new(period: usize) -> Self {
        Self {
            roc: Roc::new(period),
            prev_roc: None,
            in_position: false,
            period,
        }
    }
}

impl Strategy for RocCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.roc.update(bar.close) else {
            return vec![];
        };

        let prev = self.prev_roc.replace(v);
        let Some(p) = prev else {
            return vec![];
        };

        if p <= 0.0 && v > 0.0 && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if p >= 0.0 && v < 0.0 && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "roc"
    }

    fn reset(&mut self) {
        self.roc = Roc::new(self.period);
        self.prev_roc = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn roc_crossover_parity() {
        let bars = trending_bars(200);

        // 1. hardcoded (period=10)
        let mut hc = RocCrossover::new(10);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "roc": { "type": "roc", "period": 10 } },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "roc", "field": "value", "op": "cross_above", "value": 0.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "roc", "field": "value", "op": "cross_below", "value": 0.0 }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_roc(10) <= 0.0 && roc(10) > 0.0",
            "exit":  "prev_roc(10) >= 0.0 && roc(10) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "roc: hardcoded produced no signals");
        assert_parity("roc hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("roc hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
