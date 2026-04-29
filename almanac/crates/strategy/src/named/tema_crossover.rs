//! TEMA Crossover Strategy
//!
//! Long when fast TEMA crosses above slow TEMA.
//! Exit when fast TEMA crosses below slow TEMA.
//!
//! TEMA has less lag than EMA, so it reacts faster to trend changes.

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Tema;

pub struct TemaCrossover {
    fast: Tema,
    slow: Tema,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
}

impl TemaCrossover {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        Self {
            fast: Tema::new(fast_period),
            slow: Tema::new(slow_period),
            prev_fast: None,
            prev_slow: None,
            in_position: false,
        }
    }
}

impl Strategy for TemaCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (fast, slow) else {
            return vec![];
        };

        let mut signals = vec![];

        if let (Some(pf), Some(ps)) = (self.prev_fast, self.prev_slow) {
            let was_above = pf > ps;
            let now_above = f > s;

            if !self.in_position && !was_above && now_above {
                self.in_position = true;
                signals.push(Signal::long(bar.timestamp, &bar.symbol, 1.0));
            } else if self.in_position && was_above && !now_above {
                self.in_position = false;
                signals.push(Signal::close(bar.timestamp, &bar.symbol));
            }
        }

        self.prev_fast = Some(f);
        self.prev_slow = Some(s);
        signals
    }

    fn name(&self) -> &str { "tema_crossover" }

    fn reset(&mut self) {
        self.fast.reset();
        self.slow.reset();
        self.prev_fast = None;
        self.prev_slow = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use serde_json::json;
    use crate::factory::build_strategy;
    use crate::test_utils::*;

    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "T", close * 1.005, close * 1.005, close * 0.995, close, 1000.0)
    }

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    fn trending_bars(n: usize) -> Vec<Bar> {
        let third = n / 3;
        (0..n).map(|i| {
            let price = if i < third {
                200.0 - i as f64 * 1.5
            } else if i < third * 2 {
                200.0 - third as f64 * 1.5 + (i - third) as f64 * 2.0
            } else {
                200.0 - third as f64 * 1.5 + third as f64 * 2.0 - (i - third * 2) as f64 * 2.0
            };
            bar(i as i64 * 60_000, price.max(10.0))
        }).collect()
    }

    #[test]
    fn no_signal_before_warmup() {
        let mut s = TemaCrossover::new(10, 25);
        for i in 0..50 {
            assert!(s.on_bar(&bar(i, 100.0)).is_empty());
        }
    }

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn parity_dynamic() {
        let bars = trending_bars(300);
        let mut hc = TemaCrossover::new(10, 25);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "tema", "period": 10 },
                "slow": { "type": "tema", "period": 25 }
            },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "fast", "field": "value", "op": "cross_above",
                            "compare": "slow", "compare_field": "value" }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "fast", "field": "value", "op": "cross_below",
                            "compare": "slow", "compare_field": "value" }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "no signals produced");
        assert_eq!(hc_sigs, dyn_sigs, "hardcoded vs dynamic mismatch");
    }
    */

    #[test]
    fn parity_cel() {
        let bars = trending_bars(300);
        let mut hc = TemaCrossover::new(10, 25);
        let hc_sigs = run(&mut hc, &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_tema(10) <= prev_tema(25) && tema(10) > tema(25)",
            "exit":  "prev_tema(10) >= prev_tema(25) && tema(10) < tema(25)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert_eq!(hc_sigs, cel_sigs, "hardcoded vs cel mismatch");
    }

    #[test]
    fn parity_reset() {
        let bars = trending_bars(300);
        let mut hc = TemaCrossover::new(10, 25);
        let r1 = run(&mut hc, &bars);
        hc.reset();
        let r2 = run(&mut hc, &bars);
        assert_eq!(r1, r2, "reset parity failed");
    }

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn tema_crossover_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (fast=10, slow=25)
        let mut hc = TemaCrossover::new(10, 25);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "tema", "period": 10 },
                "slow": { "type": "tema", "period": 25 }
            },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "fast", "field": "value", "op": "cross_above",
                            "compare": "slow", "compare_field": "value" }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "fast", "field": "value", "op": "cross_below",
                            "compare": "slow", "compare_field": "value" }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_tema(10) <= prev_tema(25) && tema(10) > tema(25)",
            "exit":  "prev_tema(10) >= prev_tema(25) && tema(10) < tema(25)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "tema_crossover: hardcoded produced no signals");
        assert_parity("tema hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("tema hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
