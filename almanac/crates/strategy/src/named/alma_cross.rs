use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Alma;

/// Arnaud Legoux MA fast/slow crossover.
///
/// ALMA uses a Gaussian-weighted window that minimises lag while filtering noise.
/// Default offset=0.85, sigma=6.0 per the original specification.
///
/// Long when fast ALMA crosses above slow ALMA.
/// Close when fast ALMA crosses below slow ALMA.
pub struct AlmaCross {
    fast: Alma,
    slow: Alma,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
    fast_period: usize,
    slow_period: usize,
    offset: f64,
    sigma: f64,
}

impl AlmaCross {
    pub fn new(fast_period: usize, slow_period: usize, offset: f64, sigma: f64) -> Self {
        assert!(fast_period < slow_period, "fast must be < slow");
        Self {
            fast: Alma::new(fast_period, offset, sigma),
            slow: Alma::new(slow_period, offset, sigma),
            prev_fast: None,
            prev_slow: None,
            in_position: false,
            fast_period,
            slow_period,
            offset,
            sigma,
        }
    }
}

impl Strategy for AlmaCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (fast, slow) else {
            return vec![];
        };

        let (Some(pf), Some(ps)) = (self.prev_fast, self.prev_slow) else {
            self.prev_fast = Some(f);
            self.prev_slow = Some(s);
            return vec![];
        };

        let crossed_above = pf <= ps && f > s;
        let crossed_below = pf >= ps && f < s;
        self.prev_fast = Some(f);
        self.prev_slow = Some(s);

        if crossed_above && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "alma_cross"
    }

    fn reset(&mut self) {
        self.fast = Alma::new(self.fast_period, self.offset, self.sigma);
        self.slow = Alma::new(self.slow_period, self.offset, self.sigma);
        self.prev_fast = None;
        self.prev_slow = None;
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
    fn alma_cross_parity() {
        let bars = trending_bars(300);

        let mut hc = AlmaCross::new(9, 21, 0.85, 6.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "alma", "period": 9, "offset": 0.85, "sigma": 6.0 },
                "slow": { "type": "alma", "period": 21, "offset": 0.85, "sigma": 6.0 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_alma(9) <= prev_alma(21) && alma(9) > alma(21)",
            "exit":  "prev_alma(9) >= prev_alma(21) && alma(9) < alma(21)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "alma_cross: no signals");
        assert_parity("alma_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("alma_cross hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
