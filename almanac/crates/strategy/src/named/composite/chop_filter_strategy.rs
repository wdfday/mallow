use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Chop, Ema};

/// Chop Filter EMA Crossover Strategy.
/// Only trades EMA crossovers when the Choppiness Index is below `chop_threshold`
/// (i.e., the market is trending, not ranging).
///
/// Long when fast EMA crosses above slow EMA AND CHOP < chop_threshold.
/// Close when fast EMA crosses below slow EMA.
pub struct ChopFilterStrategy {
    chop_period: usize,
    fast_period: usize,
    slow_period: usize,
    chop_threshold: f64,
    chop: Chop,
    fast_ema: Ema,
    slow_ema: Ema,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
}

impl ChopFilterStrategy {
    pub fn new(
        chop_period: usize,
        fast_ema: usize,
        slow_ema: usize,
        chop_threshold: f64,
    ) -> Self {
        Self {
            chop_period,
            fast_period: fast_ema,
            slow_period: slow_ema,
            chop_threshold,
            chop: Chop::new(chop_period),
            fast_ema: Ema::new(fast_ema),
            slow_ema: Ema::new(slow_ema),
            prev_fast: None,
            prev_slow: None,
        }
    }
}

impl Strategy for ChopFilterStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let chop_val = self.chop.update(bar.high, bar.low, bar.close);
        let fast_val = self.fast_ema.update(bar.close);
        let slow_val = self.slow_ema.update(bar.close);

        let (Some(fast), Some(slow)) = (fast_val, slow_val) else {
            // Update prev values even when not ready
            if let Some(f) = fast_val {
                self.prev_fast = Some(f);
            }
            if let Some(s) = slow_val {
                self.prev_slow = Some(s);
            }
            return vec![];
        };

        let signal = match (self.prev_fast, self.prev_slow) {
            (Some(pf), Some(ps)) => {
                let chop_ok = chop_val.map_or(true, |c| c < self.chop_threshold);

                if pf <= ps && fast > slow && chop_ok {
                    // Fast crosses above slow in trending market → Long
                    let strength = ((fast - slow) / slow).abs().clamp(0.0, 1.0);
                    Some(Signal::long(bar.timestamp, &bar.symbol, strength))
                } else if pf >= ps && fast < slow {
                    // Fast crosses below slow → Close (regardless of CHOP)
                    Some(Signal::exit(bar.timestamp, &bar.symbol))
                } else {
                    None
                }
            }
            _ => None,
        };

        self.prev_fast = Some(fast);
        self.prev_slow = Some(slow);
        signal.into_iter().collect()
    }

    fn name(&self) -> &str {
        "chop_filter"
    }

    fn reset(&mut self) {
        self.chop = Chop::new(self.chop_period);
        self.fast_ema = Ema::new(self.fast_period);
        self.slow_ema = Ema::new(self.slow_period);
        self.prev_fast = None;
        self.prev_slow = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn test_no_signal_before_emas_ready() {
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = ChopFilterStrategy::new(5, 3, 8, 61.8);
        for i in 0..8 {
            let s = strat.on_bar(&bars[i]);
            assert!(s.is_empty(), "no signal before EMAs ready: bar {i}");
        }
    }

    #[test]
    fn test_long_in_trending_market() {
        use alm_core::signal::Direction;
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = ChopFilterStrategy::new(5, 3, 8, 100.0);
        let mut signals = Vec::new();
        for b in &bars {
            signals.extend(strat.on_bar(b));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit Long signal on real bars"
        );
    }

    #[test]
    fn test_reset_clears_state() {
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = ChopFilterStrategy::new(5, 3, 8, 61.8);
        for b in bars.iter().take(30) {
            strat.on_bar(b);
        }
        strat.reset();
        assert!(strat.prev_fast.is_none());
        assert!(strat.prev_slow.is_none());
    }

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = ChopFilterStrategy::new(14, 8, 21, 61.8);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
let ema8  = ind.ema(8);
let ema21 = ind.ema(21);
let chop14 = ind.chop(14, buf=1);
if ema8[1] <= ema21[1] && ema8[0] > ema21[0] && chop14[0] < 61.8 { entry = true; }
if ema8[1] >= ema21[1] && ema8[0] < ema21[0] { exit = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "chop_filter: must produce signals");
        assert_parity("chop_filter parity vs named", &named_sigs, &script_sigs);
    }
}
