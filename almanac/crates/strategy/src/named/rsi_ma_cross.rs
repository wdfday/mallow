use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, Rsi};

/// EMA crossover confirmed by RSI momentum filter.
///
/// Long when fast EMA crosses above slow EMA AND RSI > 50 (bullish momentum).
/// Close when fast EMA crosses below slow EMA OR RSI < 45 (momentum fading).
pub struct RsiMaCross {
    fast: Ema,
    slow: Ema,
    rsi: Rsi,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
    fast_period: usize,
    slow_period: usize,
    rsi_period: usize,
    rsi_entry: f64,
    rsi_exit: f64,
}

impl RsiMaCross {
    pub fn new(
        fast_period: usize,
        slow_period: usize,
        rsi_period: usize,
        rsi_entry: f64,
        rsi_exit: f64,
    ) -> Self {
        Self {
            fast: Ema::new(fast_period),
            slow: Ema::new(slow_period),
            rsi: Rsi::new(rsi_period),
            prev_fast: None,
            prev_slow: None,
            in_position: false,
            fast_period,
            slow_period,
            rsi_period,
            rsi_entry,
            rsi_exit,
        }
    }
}

impl Strategy for RsiMaCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);
        let rsi = self.rsi.update(bar.close);

        let (Some(f), Some(s), Some(r)) = (fast, slow, rsi) else {
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

        if crossed_above && r > self.rsi_entry && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (crossed_below || r < self.rsi_exit) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "rsi_ma_cross"
    }

    fn reset(&mut self) {
        self.fast = Ema::new(self.fast_period);
        self.slow = Ema::new(self.slow_period);
        self.rsi = Rsi::new(self.rsi_period);
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
    fn rsi_ma_cross_parity() {
        let bars = trending_bars(300);

        let mut hc = RsiMaCross::new(20, 50, 14, 50.0, 45.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema", "period": 20 },
                "slow": { "type": "ema", "period": 50 },
                "rsi":  { "type": "rsi", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" },
                { "source": "rsi", "field": "value", "op": "gt", "value": 50.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" },
                { "source": "rsi", "field": "value", "op": "lt", "value": 45.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(20) <= prev_ema(50) && ema(20) > ema(50) && rsi(14) > 50.0",
            "exit":  "(prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)) || rsi(14) < 45.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "rsi_ma_cross: no signals");
        assert_parity("rsi_ma_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("rsi_ma_cross hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
