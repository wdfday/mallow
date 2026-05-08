use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Trix;

/// Bot — TRIX Signal Line Crossover.
///
/// Long when TRIX line crosses above its signal line.
/// Close when TRIX crosses below signal line.
pub struct TrixStrategy {
    trix: Trix,
    prev_hist: Option<f64>,
    in_position: bool,
    period: usize,
    signal_period: usize,
}

impl TrixStrategy {
    pub fn new(period: usize, signal_period: usize) -> Self {
        Self {
            trix: Trix::new(period, signal_period),
            prev_hist: None,
            in_position: false,
            period,
            signal_period,
        }
    }
}

impl Strategy for TrixStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.trix.update(bar.close) else {
            return vec![];
        };

        let prev = self.prev_hist.replace(v.histogram);
        let Some(p) = prev else {
            return vec![];
        };

        // Histogram crosses above 0: TRIX crossed above signal
        if p <= 0.0 && v.histogram > 0.0 && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if p >= 0.0 && v.histogram < 0.0 && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "trix"
    }

    fn reset(&mut self) {
        self.trix = Trix::new(self.period, self.signal_period);
        self.prev_hist = None;
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
        let mut s = TrixStrategy::new(18, 9);
        for i in 0..60 {
            assert!(s.on_bar(&bar(i, 100.0)).is_empty());
        }
    }
    

    #[test]
    fn parity_reset() {
        let bars = trending_bars(300);
        let mut hc = TrixStrategy::new(18, 9);
        let r1 = run(&mut hc, &bars);
        hc.reset();
        let r2 = run(&mut hc, &bars);
        assert_eq!(r1, r2, "reset parity failed");
    }

    #[test]
    fn rhai_parity() {
        use alm_core::signal::Direction;
        let bars = slow_trend_bars();

        let mut named = TrixStrategy::new(18, 9);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let th = ind.trix_hist(18);
if th[1] <= 0.0 && th[0] > 0.0 { entry = true; }
if th[1] >= 0.0 && th[0] < 0.0 { exit = true; }
"#;
        let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
        let rhai_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| rhai.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "trix_strategy: must produce signals");
        assert_eq!(named_sigs, rhai_sigs, "rhai parity failed");
    }

}
