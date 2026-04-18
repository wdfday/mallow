use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Adx, Ema};

/// EMA crossover gated by ADX trending strength.
///
/// Long when fast EMA crosses above slow EMA AND ADX > threshold (confirmed trend).
/// Close when fast EMA crosses below slow EMA.
pub struct AdxEmaCross {
    fast: Ema,
    slow: Ema,
    adx: Adx,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
    fast_period: usize,
    slow_period: usize,
    adx_period: usize,
    adx_threshold: f64,
}

impl AdxEmaCross {
    pub fn new(
        fast_period: usize,
        slow_period: usize,
        adx_period: usize,
        adx_threshold: f64,
    ) -> Self {
        Self {
            fast: Ema::new(fast_period),
            slow: Ema::new(slow_period),
            adx: Adx::new(adx_period),
            prev_fast: None,
            prev_slow: None,
            in_position: false,
            fast_period,
            slow_period,
            adx_period,
            adx_threshold,
        }
    }
}

impl Strategy for AdxEmaCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);
        let adx_val = self.adx.update(bar.high, bar.low, bar.close);

        let (Some(f), Some(s), Some(adx)) = (fast, slow, adx_val) else {
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
        "adx_ema_cross"
    }

    fn reset(&mut self) {
        self.fast = Ema::new(self.fast_period);
        self.slow = Ema::new(self.slow_period);
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

    #[test]
    fn adx_ema_cross_parity() {
        let bars = trending_bars(300);

        let mut hc = AdxEmaCross::new(20, 50, 14, 25.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema", "period": 20 },
                "slow": { "type": "ema", "period": 50 },
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
            "entry": "prev_ema(20) <= prev_ema(50) && ema(20) > ema(50) && adx(14) > 25.0",
            "exit":  "prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "adx_ema_cross: no signals");
        assert_parity("adx_ema_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("adx_ema_cross hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
