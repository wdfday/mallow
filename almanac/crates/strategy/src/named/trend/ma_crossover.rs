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
            fast_period: fast,
            slow_period: slow,
        }
    }
}

impl Strategy for MaCrossover {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

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

        if crossed_above {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }

        if crossed_below {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
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
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let e20 = ind.ema(20);
let e50 = ind.ema(50);
if cross_above(e20, e50) { entry = true; }
if cross_below(e20, e50) { exit = true; }
"#;
#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;

    

    #[test]
    fn test_no_signal_during_warmup() {
        let Some(bars) = crate::test_utils::load_real_bars() else { return; };
        let mut strat = MaCrossover::new(3, 6);
        for b in bars.iter().take(6) {
            let sigs = strat.on_bar(b);
            assert!(sigs.is_empty(), "no signal during warmup");
        }
    }

    #[test]
    fn test_long_signal_on_cross_above() {
        let Some(bars) = crate::test_utils::load_real_bars() else { return; };
        let mut strat = MaCrossover::new(3, 8);
        let mut signals = Vec::new();
        for b in &bars {
            signals.extend(strat.on_bar(b));
        }
        use alm_core::signal::Direction;
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit a Long signal after cross above"
        );
    }

    #[test]
    fn test_reset_clears_position() {
        let Some(bars) = crate::test_utils::load_real_bars() else { return; };
        let mut strat = MaCrossover::new(3, 8);
        for b in bars.iter().take(50) {
            strat.on_bar(b);
        }
        strat.reset();
        // After reset, no signals until re-warmed
        let s = strat.on_bar(&bars[50]);
        assert!(s.is_empty());
    }

    #[test]
    fn script_parity() {
        use alm_core::signal::Direction;
        use crate::test_utils::*;
        use crate::factory::build_strategy;
        use serde_json::json;

        let Some(bars) = load_real_bars() else { return; };

        let mut named = MaCrossover::new(20, 50);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "ma_crossover: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }

}
