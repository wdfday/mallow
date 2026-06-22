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
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

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


pub(crate) const RHAI_SCRIPT: &str = r#"
let sk = ind.stoch_rsi(14);
if sk[1].k >= 0.2 && sk[0].k < 0.2 { entry = true; }
if sk[1].k <= 0.8 && sk[0].k > 0.8 { exit  = true; }
"#;
#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn test_no_signal_before_ready() {
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = StochRsiStrategy::new(5, 3, 0.2, 0.8);
        for i in 0..15 {
            let s = strat.on_bar(&bars[i]);
            assert!(s.is_empty(), "no signal before StochRSI ready: bar {i}");
        }
    }

    #[test]
    fn test_long_on_oversold() {
        use alm_core::signal::Direction;
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = StochRsiStrategy::new(5, 3, 0.2, 0.8);
        let mut signals = Vec::new();
        for b in &bars {
            signals.extend(strat.on_bar(b));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit Long in oversold condition"
        );
    }

    #[test]
    fn test_close_on_overbought() {
        use alm_core::signal::Direction;
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = StochRsiStrategy::new(5, 3, 0.2, 0.8);
        let mut signals = Vec::new();
        for b in &bars {
            signals.extend(strat.on_bar(b));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Exit),
            "should emit Close in overbought condition"
        );
    }

    #[test]
    fn test_reset_clears_state() {
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = StochRsiStrategy::new(5, 3, 0.2, 0.8);
        for b in bars.iter().take(30) {
            strat.on_bar(b);
        }
        strat.reset();
        assert!(strat.prev_k.is_none());
    }

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = StochRsiStrategy::new(14, 3, 0.2, 0.8);
        let named_sigs = run(&mut named, &bars);

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "stoch_rsi: must produce signals");
        assert_parity("stoch_rsi parity vs named", &named_sigs, &script_sigs);
    }
}
