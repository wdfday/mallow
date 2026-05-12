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
    period: usize,
}

impl VortexTrend {
    pub fn new(period: usize) -> Self {
        Self {
            vortex: Vortex::new(period),
            prev_plus: None,
            prev_minus: None,
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

        if bull_cross {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bear_cross {
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
    fn rhai_parity() {
        let bars = trending_bars(300);

        let mut named = VortexTrend::new(14);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let vx = ind.vortex(14);
if vx[1].plus_vi <= vx[1].minus_vi && vx[0].plus_vi > vx[0].minus_vi { entry = true; }
if vx[1].plus_vi >= vx[1].minus_vi && vx[0].plus_vi < vx[0].minus_vi { exit  = true; }
"#;
        let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
        let rhai_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| rhai.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "vortex_trend: must produce signals");
        assert_eq!(named_sigs, rhai_sigs, "rhai parity failed");
    }
}
