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


    #[test]
    fn produces_signals() {
        let bars = trending_bars(300);
        let mut hc = TemaCrossover::new(10, 25);
        let hc_sigs = run(&mut hc, &bars);

        assert!(!hc_sigs.is_empty(), "tema_crossover: no signals");
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

    #[test]
    fn rhai_parity() {
        use crate::factory::build_strategy;
        use serde_json::json;
        use crate::test_utils::trending_bars as tb;

        let bars = tb(300);

        let mut named = TemaCrossover::new(8, 21);
        let named_sigs = run(&mut named, &bars);

        // Exit mirrors TemaCrossover: was_above (pf > ps) && !now_above (f <= s).
        // cross_below only fires when f < s, missing the f == s edge case.
        let script = r#"
let tf = ind.tema(8);
let ts = ind.tema(21);
if cross_above(tf, ts) { entry = true; }
if tf[1] > ts[1] && tf[0] <= ts[0] { exit = true; }
"#;
        let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
        let rhai_sigs = run(rhai.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "tema_crossover: must produce signals");
        assert_eq!(named_sigs, rhai_sigs, "rhai parity failed");
    }
}
