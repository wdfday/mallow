use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Tsi;

/// TSI Crossover Strategy.
/// Long when TSI crosses above `entry_threshold`; close when TSI crosses below `exit_threshold`.
pub struct TsiStrategy {
    first: usize,
    second: usize,
    entry_threshold: f64,
    exit_threshold: f64,
    tsi: Tsi,
    prev_tsi: Option<f64>,
}

impl TsiStrategy {
    pub fn new(first: usize, second: usize, entry_threshold: f64, exit_threshold: f64) -> Self {
        Self {
            first,
            second,
            entry_threshold,
            exit_threshold,
            tsi: Tsi::new(first, second),
            prev_tsi: None,
        }
    }
}

impl Strategy for TsiStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(tsi_val) = self.tsi.update(bar.close) else {
            return vec![];
        };

        let signal = match self.prev_tsi {
            Some(prev) => {
                if prev < self.entry_threshold && tsi_val >= self.entry_threshold {
                    // Cross above entry threshold → Long
                    let strength = ((tsi_val - self.entry_threshold) / (100.0 - self.entry_threshold))
                        .clamp(0.0, 1.0);
                    Some(Signal::long(bar.timestamp, &bar.symbol, strength))
                } else if prev >= self.exit_threshold && tsi_val < self.exit_threshold {
                    // Cross below exit threshold → Close
                    Some(Signal::close(bar.timestamp, &bar.symbol))
                } else {
                    None
                }
            }
            None => None,
        };

        self.prev_tsi = Some(tsi_val);
        signal.into_iter().collect()
    }

    fn name(&self) -> &str {
        "tsi"
    }

    fn reset(&mut self) {
        self.tsi = Tsi::new(self.first, self.second);
        self.prev_tsi = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "TEST", close, close + 1.0, close - 1.0, close, 1000.0)
    }

    #[test]
    fn test_no_signal_before_tsi_ready() {
        let mut strat = TsiStrategy::new(5, 3, -25.0, 25.0);
        for i in 0..10 {
            let s = strat.on_bar(&bar(i, 100.0 + i as f64));
            assert!(s.is_empty(), "no signal before TSI ready: bar {i}");
        }
    }

    #[test]
    fn test_long_signal_in_uptrend() {
        use alm_core::signal::Direction;
        // Use a very wide threshold so the TSI zero-cross is easy to trigger
        let mut strat = TsiStrategy::new(3, 2, -50.0, 50.0);
        let mut signals = Vec::new();
        // Descend first (pushes TSI negative), then ascend (pushes TSI positive, crosses threshold)
        for i in 0..30 {
            signals.extend(strat.on_bar(&bar(i, 150.0 - i as f64 * 2.0)));
        }
        for i in 30..70 {
            signals.extend(strat.on_bar(&bar(i, 90.0 + (i - 30) as f64 * 3.0)));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit Long when TSI crosses above entry threshold"
        );
    }

    #[test]
    fn test_reset_clears_state() {
        let mut strat = TsiStrategy::new(5, 3, -25.0, 25.0);
        for i in 0..30 {
            strat.on_bar(&bar(i, 100.0 + i as f64));
        }
        strat.reset();
        assert!(strat.prev_tsi.is_none());
    }

    #[test]
    fn rhai_parity() {
        use alm_core::signal::Direction;

        let bars = rsi_bars(300);

        let mut named = TsiStrategy::new(25, 13, -25.0, 25.0);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        // ind.tsi(25) uses defaults: first=25, second=13
        let script = r#"
let tsi25 = ind.tsi(25);
if tsi25[1] < -25.0 && tsi25[0] >= -25.0 { entry = true; }
if tsi25[1] >= 25.0 && tsi25[0] < 25.0   { exit  = true; }
"#;
        let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
        let rhai_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| rhai.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "tsi: must produce signals");
        assert_eq!(named_sigs, rhai_sigs, "rhai parity failed");
    }
}
