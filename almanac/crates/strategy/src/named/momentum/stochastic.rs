use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Stochastic, StochasticValue};

/// Stochastic Crossover strategy.
///
/// Buy when %K crosses above %D in oversold zone (< 20).
/// Sell when %K crosses below %D in overbought zone (> 80).
pub struct StochasticCrossover {
    stoch: Stochastic,
    prev: Option<StochasticValue>,
    k_period: usize,
    d_period: usize,
    oversold: f64,
    overbought: f64,
}

impl StochasticCrossover {
    pub fn new(k_period: usize, d_period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            stoch: Stochastic::new(k_period, d_period),
            prev: None,
            k_period,
            d_period,
            oversold,
            overbought,
        }
    }

    /// Standard Stochastic(14, 3) with 20/80 thresholds
    pub fn standard() -> Self {
        Self::new(14, 3, 20.0, 80.0)
    }
}

impl Strategy for StochasticCrossover {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(curr) = self.stoch.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let Some(prev) = self.prev else {
            self.prev = Some(curr);
            return vec![];
        };

        let result = {
            // %K crosses above %D in oversold zone → buy
            let k_crossed_above = prev.k <= prev.d && curr.k > curr.d;
            let in_oversold = curr.d < self.oversold;

            // %K crosses below %D in overbought zone → sell
            let k_crossed_below = prev.k >= prev.d && curr.k < curr.d;
            let in_overbought = curr.d > self.overbought;

            if k_crossed_above && in_oversold {
                vec![Signal::long(bar.timestamp, &bar.symbol, curr.k / 100.0)]
            } else if k_crossed_below && in_overbought {
                vec![Signal::exit(bar.timestamp, &bar.symbol)]
            } else {
                vec![]
            }
        };

        self.prev = Some(curr);
        result
    }

    fn name(&self) -> &str {
        "stochastic_crossover"
    }

    fn reset(&mut self) {
        self.stoch = Stochastic::new(self.k_period, self.d_period);
        self.prev = None;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let st = ind.stochastic(14);
if st[1].k <= st[1].d && st[0].k > st[0].d && st[0].d < 20.0 { entry = true; }
if st[1].k >= st[1].d && st[0].k < st[0].d && st[0].d > 80.0 { exit  = true; }
"#;
#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn no_signal_before_warmup() {
        let Some(bars) = load_real_bars() else { return; };
        let mut s = StochasticCrossover::new(14, 3, 20.0, 80.0);
        for i in 0..15 {
            assert!(s.on_bar(&bars[i]).is_empty());
        }
    }

    #[test]
    fn produces_signals() {
        let Some(bars) = load_real_bars() else { return; };
        let mut hc = StochasticCrossover::new(14, 3, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);
        assert!(!hc_sigs.is_empty(), "stochastic_crossover: no signals");
    }

    #[test]
    fn parity_reset() {
        let Some(bars) = load_real_bars() else { return; };
        let mut hc = StochasticCrossover::new(14, 3, 20.0, 80.0);
        let r1 = run(&mut hc, &bars);
        hc.reset();
        let r2 = run(&mut hc, &bars);
        assert_eq!(r1, r2, "reset parity failed");
    }

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = StochasticCrossover::new(14, 3, 20.0, 80.0);
        let named_sigs = run(&mut named, &bars);

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "stochastic: must produce signals");
        assert_parity("stochastic parity vs named", &named_sigs, &script_sigs);
    }
}
