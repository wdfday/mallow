use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Adx, Ema};

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
    in_position: bool,
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
            in_position: false,
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

        if crossed_above && adx.adx > self.adx_threshold && !self.in_position {
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
        "trend_transition"
    }

    fn reset(&mut self) {
        self.fast_ema = Ema::new(self.fast_period);
        self.slow_ema = Ema::new(self.slow_period);
        self.adx = Adx::new(self.adx_period);
        self.prev_fast = None;
        self.prev_slow = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn trend_transition_parity() {
        let bars = slow_trend_bars();

        let mut hc = TrendTransition::new(50, 200, 14, 25.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema", "period": 50 },
                "slow": { "type": "ema", "period": 200 },
                "adx":  { "type": "adx", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" },
                { "source": "adx", "field": "adx", "op": "gt", "value": 25.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(50) <= prev_ema(200) && ema(50) > ema(200) && adx(14) > 25.0",
            "exit":  "prev_ema(50) >= prev_ema(200) && ema(50) < ema(200)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "trend_transition: no signals");
        assert_parity("trend_transition hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("trend_transition hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
