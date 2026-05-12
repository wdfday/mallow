use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Aroon;

/// Aroon Trend — enters when Aroon Up dominates, exits when direction reverses.
///
/// Long  when Aroon Up > `bull_threshold` AND Aroon Down < `bear_threshold`.
/// Close when Aroon Up < Aroon Down (trend reversal confirmed).
///
/// Default: period=25, bull_threshold=70, bear_threshold=30
pub struct AroonTrend {
    aroon: Aroon,
    period: usize,
    bull_threshold: f64,
    bear_threshold: f64,
}

impl AroonTrend {
    pub fn new(period: usize, bull_threshold: f64, bear_threshold: f64) -> Self {
        Self {
            aroon: Aroon::new(period),
            period,
            bull_threshold,
            bear_threshold,
        }
    }
}

impl Strategy for AroonTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.aroon.update(bar.high, bar.low) else {
            return vec![];
        };

        if v.up > self.bull_threshold && v.down < self.bear_threshold {
            let strength = (v.up - v.down) / 200.0 + 0.5;
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength.clamp(0.0, 1.0))];
        }

        // Exit when aroon up drops below down (trend lost)
        if v.up < v.down {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "aroon_trend"
    }

    fn reset(&mut self) {
        self.aroon = Aroon::new(self.period);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;

    fn bar(ts: i64, h: f64, l: f64) -> Bar {
        Bar::new(ts, "T", h, h, l, (h + l) / 2.0, 1000.0)
    }

    fn ohlcv(ts: i64, close: f64) -> Bar {
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
            ohlcv(i as i64 * 60_000, price.max(10.0))
        }).collect()
    }

    #[test]
    fn test_aroon_no_signal_warmup() {
        let mut s = AroonTrend::new(25, 70.0, 30.0);
        for i in 0..25 { assert!(s.on_bar(&bar(i, 100.0 + i as f64, 99.0)).is_empty()); }
    }
    
    #[test]
    fn parity_reset() {
        let bars = trending_bars(300);
        let mut hc = AroonTrend::new(25, 70.0, 30.0);
        let r1 = run(&mut hc, &bars);
        hc.reset();
        let r2 = run(&mut hc, &bars);
        assert_eq!(r1, r2, "reset parity failed");
    }

    #[test]
    fn rhai_parity() {
        use crate::factory::build_strategy;
        use serde_json::json;

        let bars = trending_bars(400);

        let mut named = AroonTrend::new(25, 70.0, 30.0);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let ar = ind.aroon(25, 1);
if ar[0].up > 70.0 && ar[0].down < 30.0 { entry = true; }
if ar[0].up < ar[0].down { exit = true; }
"#;
        let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
        let rhai_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| rhai.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "aroon_strategy: must produce signals");
        assert_eq!(named_sigs, rhai_sigs, "rhai parity failed");
    }

}
