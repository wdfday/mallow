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
    in_position: bool,
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
            in_position: false,
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
                if prev < self.entry_threshold && tsi_val >= self.entry_threshold && !self.in_position {
                    // Cross above entry threshold → Long
                    self.in_position = true;
                    let strength = ((tsi_val - self.entry_threshold) / (100.0 - self.entry_threshold))
                        .clamp(0.0, 1.0);
                    Some(Signal::long(bar.timestamp, &bar.symbol, strength))
                } else if prev >= self.exit_threshold && tsi_val < self.exit_threshold && self.in_position {
                    // Cross below exit threshold → Close
                    self.in_position = false;
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
        self.in_position = false;
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
        assert!(!strat.in_position);
        assert!(strat.prev_tsi.is_none());
    }

    #[test]
    fn tsi_parity() {
        let bars = trending_bars(300);

        let mut hc = TsiStrategy::new(25, 13, 0.0, 0.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "tsi": { "type": "tsi", "first": 25, "second": 13 } },
            "entry": { "logic": "and", "rules": [
                { "source": "tsi", "field": "value", "op": "cross_above", "value": 0.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "tsi", "field": "value", "op": "cross_below", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_tsi(25) <= 0.0 && tsi(25) > 0.0",
            "exit":  "prev_tsi(25) >= 0.0 && tsi(25) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "tsi: no signals");
        assert_parity("tsi hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("tsi hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
