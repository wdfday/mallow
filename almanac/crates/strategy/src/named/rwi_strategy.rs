use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Rwi;

/// Bot — Random Walk Index (Schwager).
///
/// Long when RWI_High > `threshold` (non-random uptrend detected).
/// Close when RWI_Low > `threshold` (non-random downtrend).
pub struct RwiStrategy {
    rwi: Rwi,
    threshold: f64,
    period: usize,
    in_position: bool,
}

impl RwiStrategy {
    pub fn new(period: usize, threshold: f64) -> Self {
        Self {
            rwi: Rwi::new(period),
            threshold,
            period,
            in_position: false,
        }
    }
}

impl Strategy for RwiStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.rwi.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if v.rwi_high > self.threshold && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, (v.rwi_high - 1.0).min(1.0).max(0.0))];
        }
        if v.rwi_low > self.threshold && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "rwi"
    }

    fn reset(&mut self) {
        self.rwi = Rwi::new(self.period);
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
    fn rwi_parity() {
        let bars = trending_bars(300);

        let mut hc = RwiStrategy::new(14, 1.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "rwi": { "type": "rwi", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "rwi", "field": "rwi_high", "op": "gt", "value": 1.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "rwi", "field": "rwi_low", "op": "gt", "value": 1.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "rwi_high(14) > 1.0",
            "exit":  "rwi_low(14) > 1.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "rwi: no signals");
        assert_parity("rwi hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("rwi hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
