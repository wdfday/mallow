use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::StochasticRsi;

/// Stochastic RSI Strategy.
/// Long when StochRSI K crosses below `oversold`; close when K crosses above `overbought`.
pub struct StochRsiStrategy {
    rsi_period: usize,
    smooth_d: usize,
    oversold: f64,
    overbought: f64,
    srsi: StochasticRsi,
    prev_k: Option<f64>,
}

impl StochRsiStrategy {
    pub fn new(rsi_period: usize, smooth_d: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            rsi_period,
            smooth_d,
            oversold,
            overbought,
            srsi: StochasticRsi::new(rsi_period, smooth_d),
            prev_k: None,
        }
    }
}

impl Strategy for StochRsiStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.srsi.update(bar.close) else {
            return vec![];
        };

        let signal = match self.prev_k {
            Some(prev) => {
                if prev >= self.oversold && v.k < self.oversold {
                    // K drops below oversold → Long (mean reversion)
                    let strength = ((self.oversold - v.k) / self.oversold).clamp(0.0, 1.0);
                    Some(Signal::long(bar.timestamp, &bar.symbol, strength))
                } else if prev <= self.overbought && v.k > self.overbought {
                    // K rises above overbought → Close
                    Some(Signal::exit(bar.timestamp, &bar.symbol))
                } else {
                    None
                }
            }
            None => None,
        };

        self.prev_k = Some(v.k);
        signal.into_iter().collect()
    }

    fn name(&self) -> &str {
        "stoch_rsi"
    }

    fn reset(&mut self) {
        self.srsi = StochasticRsi::new(self.rsi_period, self.smooth_d);
        self.prev_k = None;
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
    fn test_no_signal_before_ready() {
        let mut strat = StochRsiStrategy::new(5, 3, 0.2, 0.8);
        for i in 0..15 {
            let s = strat.on_bar(&bar(i, 100.0 + i as f64));
            assert!(s.is_empty(), "no signal before StochRSI ready: bar {i}");
        }
    }

    #[test]
    fn test_long_on_oversold() {
        use alm_core::signal::Direction;
        // Use a smaller rsi_period so RSI window variation is easier to achieve
        let mut strat = StochRsiStrategy::new(5, 3, 0.2, 0.8);
        let mut signals = Vec::new();
        // Oscillate to build RSI variation, then plunge to drop RSI (StochRSI goes to 0)
        let mixed: Vec<f64> = vec![
            100.0, 105.0, 98.0, 107.0, 96.0, 110.0, 100.0, 105.0, 98.0, 107.0,
            96.0, 110.0, 100.0, 105.0, 98.0, 107.0, 96.0, 110.0, 100.0, 105.0,
        ];
        for (i, &p) in mixed.iter().enumerate() {
            signals.extend(strat.on_bar(&bar(i as i64, p)));
        }
        // Now plunge → RSI drops sharply below past RSI values → StochRSI K drops below 0.2
        for i in 0..15 {
            signals.extend(strat.on_bar(&bar((mixed.len() + i) as i64, 110.0 - i as f64 * 4.0)));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit Long in oversold condition"
        );
    }

    #[test]
    fn test_close_on_overbought() {
        use alm_core::signal::Direction;
        let mut strat = StochRsiStrategy::new(5, 3, 0.2, 0.8);
        let mut signals = Vec::new();
        // Oscillate to build RSI variation
        let mixed: Vec<f64> = vec![
            100.0, 105.0, 98.0, 107.0, 96.0, 110.0, 100.0, 105.0, 98.0, 107.0,
            96.0, 110.0, 100.0, 105.0, 98.0, 107.0, 96.0, 110.0, 100.0, 105.0,
        ];
        for (i, &p) in mixed.iter().enumerate() {
            signals.extend(strat.on_bar(&bar(i as i64, p)));
        }
        let offset = mixed.len() as i64;
        // Plunge to trigger Long
        for i in 0..15 {
            signals.extend(strat.on_bar(&bar(offset + i, 110.0 - i as f64 * 4.0)));
        }
        // Rally sharply → RSI rises above previous RSI values → StochRSI K rises above 0.8
        for i in 0..20 {
            signals.extend(strat.on_bar(&bar(offset + 15 + i, 50.0 + i as f64 * 5.0)));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Exit),
            "should emit Close in overbought condition"
        );
    }

    #[test]
    fn test_reset_clears_state() {
        let mut strat = StochRsiStrategy::new(5, 3, 0.2, 0.8);
        for i in 0..30 {
            strat.on_bar(&bar(i, 100.0 + i as f64));
        }
        strat.reset();
        assert!(strat.prev_k.is_none());
    }

    #[test]
    fn script_parity() {
        use alm_core::signal::Direction;

        let bars = stoch_rsi_bars();

        let mut named = StochRsiStrategy::new(14, 3, 0.2, 0.8);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let sk = ind.stoch_rsi(14);
if sk[1].k >= 0.2 && sk[0].k < 0.2 { entry = true; }
if sk[1].k <= 0.8 && sk[0].k > 0.8 { exit  = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "stoch_rsi: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
