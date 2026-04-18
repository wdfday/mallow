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
    in_position: bool,
}

impl AroonTrend {
    pub fn new(period: usize, bull_threshold: f64, bear_threshold: f64) -> Self {
        Self {
            aroon: Aroon::new(period),
            period,
            bull_threshold,
            bear_threshold,
            in_position: false,
        }
    }
}

impl Strategy for AroonTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.aroon.update(bar.high, bar.low) else {
            return vec![];
        };

        if v.up > self.bull_threshold && v.down < self.bear_threshold && !self.in_position {
            self.in_position = true;
            let strength = (v.up - v.down) / 200.0 + 0.5;
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength.clamp(0.0, 1.0))];
        }

        // Exit when aroon up drops below down (trend lost)
        if v.up < v.down && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "aroon_trend"
    }

    fn reset(&mut self) {
        self.aroon = Aroon::new(self.period);
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
    fn parity_dynamic() {
        let bars = trending_bars(300);
        let mut hc = AroonTrend::new(25, 70.0, 30.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "aroon": { "type": "aroon", "period": 25 } },
            "entry": {
                "logic": "and",
                "rules": [
                    { "source": "aroon", "field": "up",  "op": "gt", "value": 70.0 },
                    { "source": "aroon", "field": "down", "op": "lt", "value": 30.0 }
                ]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "aroon", "field": "up", "op": "lt",
                            "compare": "aroon", "compare_field": "down" }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);
        assert!(!hc_sigs.is_empty(), "no signals produced");
        assert_eq!(hc_sigs, dyn_sigs, "hardcoded vs dynamic mismatch");
    }

    #[test]
    fn parity_cel() {
        let bars = trending_bars(300);
        let mut hc = AroonTrend::new(25, 70.0, 30.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "aroon_up(25) > 70.0 && aroon_down(25) < 30.0",
            "exit":  "aroon_up(25) < aroon_down(25)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);
        assert_eq!(hc_sigs, cel_sigs, "hardcoded vs cel mismatch");
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
    fn aroon_trend_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (period=25, bull=70, bear=30)
        let mut hc = AroonTrend::new(25, 70.0, 30.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — entry: up>70 AND down<30; exit: up<down (plain lt, no cross)
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "aroon": { "type": "aroon", "period": 25 } },
            "entry": {
                "logic": "and",
                "rules": [
                    { "source": "aroon", "field": "up",   "op": "gt", "value": 70.0 },
                    { "source": "aroon", "field": "down",  "op": "lt", "value": 30.0 }
                ]
            },
            "exit": {
                "logic": "and",
                "rules": [
                    { "source": "aroon", "field": "up", "op": "lt",
                      "compare": "aroon", "compare_field": "down" }
                ]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "aroon_up(25) > 70.0 && aroon_down(25) < 30.0",
            "exit":  "aroon_up(25) < aroon_down(25)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "aroon_trend: hardcoded produced no signals");
        assert_parity("aroon hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("aroon hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }
}
