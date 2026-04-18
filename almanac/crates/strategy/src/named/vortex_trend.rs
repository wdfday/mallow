use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Vortex;

/// Vortex Indicator +VI / -VI crossover trend strategy.
///
/// Long when +VI crosses above -VI (upward vortex dominates).
/// Close when -VI crosses above +VI (downward vortex dominates).
pub struct VortexTrend {
    vortex: Vortex,
    prev_plus: Option<f64>,
    prev_minus: Option<f64>,
    in_position: bool,
    period: usize,
}

impl VortexTrend {
    pub fn new(period: usize) -> Self {
        Self {
            vortex: Vortex::new(period),
            prev_plus: None,
            prev_minus: None,
            in_position: false,
            period,
        }
    }
}

impl Strategy for VortexTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(vv) = self.vortex.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let (Some(pp), Some(pm)) = (self.prev_plus, self.prev_minus) else {
            self.prev_plus = Some(vv.plus_vi);
            self.prev_minus = Some(vv.minus_vi);
            return vec![];
        };

        let bull_cross = pp <= pm && vv.plus_vi > vv.minus_vi;
        let bear_cross = pp >= pm && vv.plus_vi < vv.minus_vi;

        self.prev_plus = Some(vv.plus_vi);
        self.prev_minus = Some(vv.minus_vi);

        if bull_cross && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bear_cross && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "vortex_trend"
    }

    fn reset(&mut self) {
        self.vortex = Vortex::new(self.period);
        self.prev_plus = None;
        self.prev_minus = None;
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
    fn vortex_trend_parity() {
        let bars = trending_bars(300);

        let mut hc = VortexTrend::new(14);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "vx": { "type": "vortex", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "vx", "field": "plus_vi", "op": "cross_above",
                  "compare": "vx", "compare_field": "minus_vi" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "vx", "field": "plus_vi", "op": "cross_below",
                  "compare": "vx", "compare_field": "minus_vi" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_vortex_plus(14) <= prev_vortex_minus(14) && vortex_plus(14) > vortex_minus(14)",
            "exit":  "prev_vortex_plus(14) >= prev_vortex_minus(14) && vortex_plus(14) < vortex_minus(14)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "vortex_trend: no signals");
        assert_parity("vortex_trend hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("vortex_trend hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
