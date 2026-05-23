use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Adx, Ema};

const RHAI: &str = r#"
let e50  = ind.ema(50);
let e200 = ind.ema(200);
let adx14 = ind.adx(14, buf=1);
if cross_above(e50, e200) && adx14[0] > 25.0 { entry = true; }
if cross_below(e50, e200) { exit = true; }
"#;

/// Bot #34 — Trend Transition Tracker.
///
/// Long when fast EMA crosses above slow EMA AND ADX > threshold (trend is strong).
/// Closes when fast EMA crosses back below slow EMA.
pub struct TrendTransition {
    fast_ema: Ema,
    slow_ema: Ema,
    adx: Adx,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    adx_threshold: f64,
    fast_period: usize,
    slow_period: usize,
    adx_period: usize,
}

impl TrendTransition {
    pub fn new(fast_period: usize, slow_period: usize, adx_period: usize, adx_threshold: f64) -> Self {
        Self {
            fast_ema: Ema::new(fast_period),
            slow_ema: Ema::new(slow_period),
            adx: Adx::new(adx_period),
            prev_fast: None,
            prev_slow: None,
            adx_threshold,
            fast_period,
            slow_period,
            adx_period,
        }
    }
}

impl Strategy for TrendTransition {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast_val = self.fast_ema.update(bar.close);
        let slow_val = self.slow_ema.update(bar.close);
        let adx_val = self.adx.update(bar.high, bar.low, bar.close);

        let (Some(f), Some(s), Some(adx)) = (fast_val, slow_val, adx_val) else {
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

        if crossed_above && adx.adx > self.adx_threshold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "trend_transition"
    }

    fn description(&self) -> &'static str {
        "Long when fast EMA crosses above slow EMA with ADX confirming a strong trend. Exit on EMA cross-down."
    }

    fn script(&self) -> Option<&'static str> { Some(RHAI) }

    fn reset(&mut self) {
        self.fast_ema = Ema::new(self.fast_period);
        self.slow_ema = Ema::new(self.slow_period);
        self.adx = Adx::new(self.adx_period);
        self.prev_fast = None;
        self.prev_slow = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    #[test]
    fn script_parity() {
        // Uses slow_trend_bars to force EMA(50) cross above EMA(200).
        let bars = slow_trend_bars();

        let mut named = TrendTransition::new(50, 200, 14, 25.0);
        let named_sigs = run(&mut named, &bars);

        let script = TrendTransition::new(50, 200, 14, 25.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "trend_transition: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
