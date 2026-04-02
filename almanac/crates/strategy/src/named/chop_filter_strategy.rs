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
    in_position: bool,
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
            in_position: false,
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

                if pf <= ps && fast > slow && !self.in_position && chop_ok {
                    // Fast crosses above slow in trending market → Long
                    self.in_position = true;
                    let strength = ((fast - slow) / slow).abs().clamp(0.0, 1.0);
                    Some(Signal::long(bar.timestamp, &bar.symbol, strength))
                } else if pf >= ps && fast < slow && self.in_position {
                    // Fast crosses below slow → Close (regardless of CHOP)
                    self.in_position = false;
                    Some(Signal::close(bar.timestamp, &bar.symbol))
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
        self.in_position = false;
        self.prev_fast = None;
        self.prev_slow = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;

    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "TEST", close, close + 1.0, close - 1.0, close, 1000.0)
    }

    #[test]
    fn test_no_signal_before_emas_ready() {
        let mut strat = ChopFilterStrategy::new(5, 3, 8, 61.8);
        for i in 0..8 {
            let s = strat.on_bar(&bar(i, 100.0 + i as f64));
            assert!(s.is_empty(), "no signal before EMAs ready: bar {i}");
        }
    }

    #[test]
    fn test_long_in_trending_market() {
        use alm_core::signal::Direction;
        // Use a very wide CHOP threshold (100 = always trade) to ensure the filter never blocks
        let mut strat = ChopFilterStrategy::new(5, 3, 8, 100.0);
        let mut signals = Vec::new();
        // Flat start to seed EMAs at the same level
        for i in 0..20 {
            signals.extend(strat.on_bar(&bar(i, 100.0)));
        }
        // Strong uptrend should pull fast EMA above slow EMA
        for i in 20..80 {
            signals.extend(strat.on_bar(&bar(i, 100.0 + (i - 20) as f64 * 1.0)));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit Long when fast EMA crosses above slow EMA in trending market"
        );
    }

    #[test]
    fn test_reset_clears_state() {
        let mut strat = ChopFilterStrategy::new(5, 3, 8, 61.8);
        for i in 0..30 {
            strat.on_bar(&bar(i, 100.0 + i as f64));
        }
        strat.reset();
        assert!(!strat.in_position);
        assert!(strat.prev_fast.is_none());
        assert!(strat.prev_slow.is_none());
    }
}
