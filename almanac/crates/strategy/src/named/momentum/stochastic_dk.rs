use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Stochastic;

pub(crate) const RHAI: &str = r#"
let st = ind.stochastic(14);
if st[1].k <= st[1].d && st[0].k > st[0].d { entry = true; }
if st[1].k >= st[1].d && st[0].k < st[0].d { exit  = true; }
"#;

/// Bot #48 — Stochastic %D/%K crossover.
///
/// Pure crossover — no overbought/oversold zone restriction.
/// Long when %K crosses above %D; closes when %K crosses below %D.
pub struct StochasticDk {
    stoch: Stochastic,
    prev_k: Option<f64>,
    prev_d: Option<f64>,
    k_period: usize,
    d_period: usize,
}

impl StochasticDk {
    pub fn new(k_period: usize, d_period: usize) -> Self {
        Self {
            stoch: Stochastic::new(k_period, d_period),
            prev_k: None,
            prev_d: None,
            k_period,
            d_period,
        }
    }
}

impl Strategy for StochasticDk {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.stoch.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let (Some(pk), Some(pd)) = (self.prev_k, self.prev_d) else {
            self.prev_k = Some(v.k);
            self.prev_d = Some(v.d);
            return vec![];
        };

        let crossed_up = pk <= pd && v.k > v.d;
        let crossed_down = pk >= pd && v.k < v.d;

        self.prev_k = Some(v.k);
        self.prev_d = Some(v.d);

        if crossed_up  {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_down {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "stochastic_dk"
    }

    fn description(&self) -> &'static str {
        "Long when Stochastic %K crosses above %D. Exit when %K crosses back below %D."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI)
    }

    fn reset(&mut self) {
        self.stoch = Stochastic::new(self.k_period, self.d_period);
        self.prev_k = None;
        self.prev_d = None;
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
        let Some(bars) = load_real_bars() else { return; };

        let mut named = StochasticDk::new(14, 3);
        let named_sigs = run(&mut named, &bars);

        let script = StochasticDk::new(14, 3).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "stochastic_dk: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
