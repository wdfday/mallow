use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Lsma;

/// Least Squares MA fast/slow crossover.
///
/// LSMA fits a linear regression to the last N bars and returns the endpoint,
/// giving lower lag than EMA while remaining smooth.
///
/// Long when fast LSMA crosses above slow LSMA.
/// Close when fast LSMA crosses below slow LSMA.
pub struct LsmaCross {
    fast: Lsma,
    slow: Lsma,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    fast_period: usize,
    slow_period: usize,
}

impl LsmaCross {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        assert!(fast_period < slow_period, "fast must be < slow");
        Self {
            fast: Lsma::new(fast_period),
            slow: Lsma::new(slow_period),
            prev_fast: None,
            prev_slow: None,
            fast_period,
            slow_period,
        }
    }
}

impl Strategy for LsmaCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (fast, slow) else {
            return vec![];
        };

        let fv = f.value;
        let sv = s.value;

        let (Some(pf), Some(ps)) = (self.prev_fast, self.prev_slow) else {
            self.prev_fast = Some(fv);
            self.prev_slow = Some(sv);
            return vec![];
        };

        let crossed_above = pf <= ps && fv > sv;
        let crossed_below = pf >= ps && fv < sv;
        self.prev_fast = Some(fv);
        self.prev_slow = Some(sv);

        if crossed_above {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "lsma_cross"
    }

    fn reset(&mut self) {
        self.fast = Lsma::new(self.fast_period);
        self.slow = Lsma::new(self.slow_period);
        self.prev_fast = None;
        self.prev_slow = None;
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
        let Some(bars) = load_real_bars() else { return; };

        let mut named = LsmaCross::new(20, 50);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let l20 = ind.lsma(20);
let l50 = ind.lsma(50);
if cross_above(l20, l50) { entry = true; }
if cross_below(l20, l50) { exit = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "lsma_cross: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
