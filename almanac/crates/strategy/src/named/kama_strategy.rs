use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Kama;

/// KAMA Crossover Strategy.
/// Long when close crosses above KAMA; close when close crosses below KAMA.
pub struct KamaStrategy {
    er_period: usize,
    fast: usize,
    slow: usize,
    kama: Kama,
    in_position: bool,
    prev_above: Option<bool>,
}

impl KamaStrategy {
    pub fn new(er_period: usize, fast: usize, slow: usize) -> Self {
        Self {
            er_period,
            fast,
            slow,
            kama: Kama::new(er_period, fast, slow),
            in_position: false,
            prev_above: None,
        }
    }
}

impl Strategy for KamaStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(kama_val) = self.kama.update(bar.close) else {
            return vec![];
        };

        let above = bar.close > kama_val;

        let signal = match self.prev_above {
            Some(was_above) => {
                if above && !was_above && !self.in_position {
                    // Cross above KAMA → Long
                    self.in_position = true;
                    let strength = ((bar.close - kama_val) / kama_val).clamp(0.0, 1.0);
                    Some(Signal::long(bar.timestamp, &bar.symbol, strength))
                } else if !above && was_above && self.in_position {
                    // Cross below KAMA → Close
                    self.in_position = false;
                    Some(Signal::close(bar.timestamp, &bar.symbol))
                } else {
                    None
                }
            }
            None => None,
        };

        self.prev_above = Some(above);
        signal.into_iter().collect()
    }

    fn name(&self) -> &str {
        "kama"
    }

    fn reset(&mut self) {
        self.kama = Kama::new(self.er_period, self.fast, self.slow);
        self.in_position = false;
        self.prev_above = None;
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
    fn test_no_signal_before_kama_ready() {
        let mut strat = KamaStrategy::new(10, 2, 30);
        for i in 0..11 {
            let s = strat.on_bar(&bar(i, 100.0 + i as f64));
            assert!(s.is_empty(), "no signal before KAMA ready: bar {i}");
        }
    }

    #[test]
    fn test_long_signal_on_cross_above() {
        use alm_core::signal::Direction;
        let mut strat = KamaStrategy::new(5, 2, 10);
        let mut signals = Vec::new();
        // Seed with flat prices to let KAMA settle
        for i in 0..15 {
            signals.extend(strat.on_bar(&bar(i, 100.0)));
        }
        // Then push price well above KAMA to trigger cross
        for i in 15..30 {
            signals.extend(strat.on_bar(&bar(i, 110.0 + (i - 15) as f64)));
        }
        // Then drop back below
        for i in 30..40 {
            signals.extend(strat.on_bar(&bar(i, 80.0)));
        }
        // We should have seen at least one Close signal
        assert!(
            signals.iter().any(|s| s.direction == Direction::Close),
            "should emit Close on cross below KAMA"
        );
    }

    #[test]
    fn test_reset_clears_state() {
        let mut strat = KamaStrategy::new(5, 2, 10);
        for i in 0..20 {
            strat.on_bar(&bar(i, 100.0 + i as f64));
        }
        strat.reset();
        assert!(!strat.in_position);
        assert!(strat.prev_above.is_none());
    }

    #[test]
    fn kama_parity() {
        let bars = trending_bars(300);

        let mut hc = KamaStrategy::new(10, 2, 30);
        let hc_sigs = run(&mut hc, &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(1) <= prev_kama(10) && ema(1) > kama(10)",
            "exit":  "prev_ema(1) >= prev_kama(10) && ema(1) < kama(10)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "kama: no signals");
        assert_parity("kama hc vs cel", &hc_sigs, &cel_sigs);
    }

    #[test]
    fn kama_dynamic_cel_parity() {
        let bars = trending_bars(300);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "kama": { "type": "kama", "er_period": 10, "fast": 2, "slow": 30 } },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "gt", "compare": "kama" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "lt", "compare": "kama" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > kama(10)",
            "exit":  "close < kama(10)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert_parity("kama dynamic vs cel (level)", &dyn_sigs, &cel_sigs);
    }
}
