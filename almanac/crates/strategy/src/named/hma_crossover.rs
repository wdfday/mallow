use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Hma;

/// Bot — HMA Crossover (Hull Moving Average).
///
/// Long when fast HMA crosses above slow HMA.
/// Close when fast HMA crosses below slow HMA.
pub struct HmaCrossover {
    fast: Hma,
    slow: Hma,
    fast_period: usize,
    slow_period: usize,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
}

impl HmaCrossover {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        assert!(fast_period < slow_period, "fast_period must be < slow_period");
        Self {
            fast: Hma::new(fast_period),
            slow: Hma::new(slow_period),
            fast_period,
            slow_period,
            prev_fast: None,
            prev_slow: None,
            in_position: false,
        }
    }
}

impl Strategy for HmaCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let f = self.fast.update(bar.close);
        let s = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (f, s) else {
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
        "hma_crossover"
    }

    fn reset(&mut self) {
        self.fast = Hma::new(self.fast_period);
        self.slow = Hma::new(self.slow_period);
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
        let mut s = HmaCrossover::new(9, 21);
        for i in 0..30 {
            assert!(s.on_bar(&bar(i, 100.0)).is_empty());
        }
    }

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn parity_dynamic() {
        let bars = trending_bars(300);
        let mut hc = HmaCrossover::new(9, 21);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "hma", "period": 9 },
                "slow": { "type": "hma", "period": 21 }
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
        let mut hc = HmaCrossover::new(9, 21);
        let hc_sigs = run(&mut hc, &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_hma(9) <= prev_hma(21) && hma(9) > hma(21)",
            "exit":  "prev_hma(9) >= prev_hma(21) && hma(9) < hma(21)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert_eq!(hc_sigs, cel_sigs, "hardcoded vs cel mismatch");
    }

    #[test]
    fn parity_reset() {
        let bars = trending_bars(300);
        let mut hc = HmaCrossover::new(9, 21);
        let r1 = run(&mut hc, &bars);
        hc.reset();
        let r2 = run(&mut hc, &bars);
        assert_eq!(r1, r2, "reset parity failed");
    }

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn hma_crossover_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (fast=9, slow=21)
        let mut hc = HmaCrossover::new(9, 21);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "hma", "period": 9 },
                "slow": { "type": "hma", "period": 21 }
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
            "entry": "prev_hma(9) <= prev_hma(21) && hma(9) > hma(21)",
            "exit":  "prev_hma(9) >= prev_hma(21) && hma(9) < hma(21)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "hma_crossover: hardcoded produced no signals");
        assert_parity("hma hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("hma hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
