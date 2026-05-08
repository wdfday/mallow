use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Stochastic, StochasticValue};

/// Stochastic Crossover strategy.
///
/// Buy when %K crosses above %D in oversold zone (< 20).
/// Sell when %K crosses below %D in overbought zone (> 80).
pub struct StochasticCrossover {
    stoch: Stochastic,
    prev: Option<StochasticValue>,
    in_position: bool,
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
            in_position: false,
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

            if k_crossed_above && in_oversold && !self.in_position {
                self.in_position = true;
                vec![Signal::long(bar.timestamp, &bar.symbol, curr.k / 100.0)]
            } else if k_crossed_below && in_overbought && self.in_position {
                self.in_position = false;
                vec![Signal::close(bar.timestamp, &bar.symbol)]
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
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;

    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "T", close * 1.005, close * 1.005, close * 0.995, close, 1000.0)
    }

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    // V-shape: falling → rising to push stochastic into oversold then overbought
    fn v_bars(n: usize) -> Vec<Bar> {
        (0..n).map(|i| {
            let price = if i < n / 2 {
                150.0 - i as f64 * 3.0
            } else {
                150.0 - (n / 2) as f64 * 3.0 + (i - n / 2) as f64 * 4.0
            };
            bar(i as i64 * 60_000, price.max(1.0))
        }).collect()
    }

    #[test]
    fn no_signal_before_warmup() {
        let mut s = StochasticCrossover::new(14, 3, 20.0, 80.0);
        for i in 0..15 {
            assert!(s.on_bar(&bar(i, 100.0)).is_empty());
        }
    }

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn parity_dynamic() {
        let bars = v_bars(150);
        let mut hc = StochasticCrossover::new(14, 3, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "stoch": { "type": "stochastic", "k_period": 14, "d_period": 3 } },
            "entry": {
                "logic": "and",
                "rules": [
                    { "source": "stoch", "field": "k", "op": "cross_above",
                      "compare": "stoch", "compare_field": "d" },
                    { "source": "stoch", "field": "d", "op": "lt", "value": 20.0 }
                ]
            },
            "exit": {
                "logic": "and",
                "rules": [
                    { "source": "stoch", "field": "k", "op": "cross_below",
                      "compare": "stoch", "compare_field": "d" },
                    { "source": "stoch", "field": "d", "op": "gt", "value": 80.0 }
                ]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "no signals produced");
        assert_eq!(hc_sigs, dyn_sigs, "hardcoded vs dynamic mismatch");
    }
    */

    #[test]
    fn produces_signals() {
        let bars = v_bars(150);
        let mut hc = StochasticCrossover::new(14, 3, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        assert!(!hc_sigs.is_empty(), "stochastic_crossover: no signals");
    }

    #[test]
    fn parity_reset() {
        let bars = v_bars(150);
        let mut hc = StochasticCrossover::new(14, 3, 20.0, 80.0);
        let r1 = run(&mut hc, &bars);
        hc.reset();
        let r2 = run(&mut hc, &bars);
        assert_eq!(r1, r2, "reset parity failed");
    }

    #[test]
    fn rhai_parity() {
        use crate::factory::build_strategy;
        use serde_json::json;

        let bars = v_bars(300);

        let mut named = StochasticCrossover::new(14, 3, 20.0, 80.0);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
let sk = ind.stoch_k(14);
let sd = ind.stoch_d(14);
if sk[1] <= sd[1] && sk[0] > sd[0] && sd[0] < 20.0 { entry = true; }
if sk[1] >= sd[1] && sk[0] < sd[0] && sd[0] > 80.0 { exit  = true; }
"#;
        let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
        let rhai_sigs = run(rhai.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "stochastic: must produce signals");
        assert_eq!(named_sigs, rhai_sigs, "rhai parity failed");
    }

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn stochastic_crossover_parity() {
        let bars = rsi_bars(150);

        // 1. hardcoded (k=14, d=3, oversold=20, overbought=80)
        let mut hc = StochasticCrossover::new(14, 3, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — K cross_above D while D<20; exit K cross_below D while D>80
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "stoch": { "type": "stochastic", "k_period": 14, "d_period": 3 } },
            "entry": {
                "logic": "and",
                "rules": [
                    { "source": "stoch", "field": "k", "op": "cross_above",
                      "compare": "stoch", "compare_field": "d" },
                    { "source": "stoch", "field": "d", "op": "lt", "value": 20.0 }
                ]
            },
            "exit": {
                "logic": "and",
                "rules": [
                    { "source": "stoch", "field": "k", "op": "cross_below",
                      "compare": "stoch", "compare_field": "d" },
                    { "source": "stoch", "field": "d", "op": "gt", "value": 80.0 }
                ]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL

        assert!(!hc_sigs.is_empty(), "stochastic_crossover: hardcoded produced no signals");
        assert_parity("stoch hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
    }
    */
}
