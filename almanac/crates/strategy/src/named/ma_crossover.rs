use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Ema;

/// Moving Average Crossover strategy.
/// Goes long when fast EMA crosses above slow EMA; closes when it crosses below.
/// Uses EMA for smoother signals; swap to SMA if preferred.
pub struct MaCrossover {
    fast: Ema,
    slow: Ema,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
    fast_period: usize,
    slow_period: usize,
}

impl MaCrossover {
    pub fn new(fast: usize, slow: usize) -> Self {
        assert!(fast < slow, "fast period must be less than slow period");
        Self {
            fast: Ema::new(fast),
            slow: Ema::new(slow),
            prev_fast: None,
            prev_slow: None,
            in_position: false,
            fast_period: fast,
            slow_period: slow,
        }
    }
}

impl Strategy for MaCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (fast, slow) else {
            return vec![];
        };

        let (Some(pf), Some(ps)) = (self.prev_fast, self.prev_slow) else {
            self.prev_fast = Some(f);
            self.prev_slow = Some(s);
            return vec![];
        };

        let crossed_above = pf <= ps && f > s;
        let crossed_below = pf >= ps && f < s;

        self.prev_fast = Some(f);
        self.prev_slow = Some(s);

        if crossed_above && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }

        if crossed_below && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "ma_crossover"
    }

    fn reset(&mut self) {
        self.fast = Ema::new(self.fast_period);
        self.slow = Ema::new(self.slow_period);
        self.prev_fast = None;
        self.prev_slow = None;
        self.in_position = false;
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
    fn test_no_signal_during_warmup() {
        let mut strat = MaCrossover::new(3, 6);
        for i in 0..6 {
            let sigs = strat.on_bar(&bar(i, 100.0 + i as f64));
            assert!(sigs.is_empty(), "no signal during warmup at bar {i}");
        }
    }

    #[test]
    fn test_long_signal_on_cross_above() {
        let mut strat = MaCrossover::new(3, 8);
        // First feed descending prices to get fast < slow
        let mut signals = Vec::new();
        for i in 0..20 {
            let price = 200.0 - i as f64 * 2.0; // descending
            let s = strat.on_bar(&bar(i, price));
            signals.extend(s);
        }
        // Now feed ascending prices to trigger crossover
        for i in 20..50 {
            let price = 160.0 + (i - 20) as f64 * 3.0;
            let s = strat.on_bar(&bar(i, price));
            signals.extend(s);
        }
        // Should have at least one Long signal
        use alm_core::signal::Direction;
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit a Long signal after cross above"
        );
    }

    #[test]
    fn test_reset_clears_position() {
        let mut strat = MaCrossover::new(3, 8);
        for i in 0..50 {
            strat.on_bar(&bar(i, 100.0 + i as f64 * 2.0));
        }
        strat.reset();
        // After reset, no signals until re-warmed
        let s = strat.on_bar(&bar(100, 200.0));
        assert!(s.is_empty());
    }
}
