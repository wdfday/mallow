use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Fisher;

/// Fisher Transform signal line crossover.
///
/// Long when Fisher crosses above its signal line from below.
/// Close when Fisher crosses below its signal line.
pub struct FisherCrossover {
    fisher: Fisher,
    prev_fisher: Option<f64>,
    prev_signal: Option<f64>,
    period: usize,
}

impl FisherCrossover {
    pub fn new(period: usize) -> Self {
        Self {
            fisher: Fisher::new(period),
            prev_fisher: None,
            prev_signal: None,
            period,
        }
    }
}

impl Strategy for FisherCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(fv) = self.fisher.update(bar.high, bar.low) else {
            return vec![];
        };

        let (Some(pf), Some(ps)) = (self.prev_fisher, self.prev_signal) else {
            self.prev_fisher = Some(fv.fisher);
            self.prev_signal = Some(fv.signal);
            return vec![];
        };

        let crossed_above = pf <= ps && fv.fisher > fv.signal;
        let crossed_below = pf >= ps && fv.fisher < fv.signal;

        self.prev_fisher = Some(fv.fisher);
        self.prev_signal = Some(fv.signal);

        if crossed_above {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "fisher_crossover"
    }

    fn reset(&mut self) {
        self.fisher = Fisher::new(self.period);
        self.prev_fisher = None;
        self.prev_signal = None;
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

        let mut named = FisherCrossover::new(10);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let fi = ind.fisher(10);
if fi[1].fisher <= fi[1].signal && fi[0].fisher > fi[0].signal { entry = true; }
if fi[1].fisher >= fi[1].signal && fi[0].fisher < fi[0].signal { exit  = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "fisher_crossover: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}