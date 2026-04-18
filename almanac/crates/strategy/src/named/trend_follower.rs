use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Macd, Sma};

/// Bot #33 — Trend Follower.
///
/// Long when fast SMA crosses above slow SMA AND MACD histogram confirms
/// positive momentum.  Exits when SMA crosses back down OR histogram turns negative.
pub struct TrendFollower {
    fast_ma: Sma,
    slow_ma: Sma,
    macd: Macd,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
    fast_period: usize,
    slow_period: usize,
    macd_fast: usize,
    macd_slow: usize,
    macd_signal: usize,
}

impl TrendFollower {
    pub fn new(
        fast_period: usize,
        slow_period: usize,
        macd_fast: usize,
        macd_slow: usize,
        macd_signal: usize,
    ) -> Self {
        Self {
            fast_ma: Sma::new(fast_period),
            slow_ma: Sma::new(slow_period),
            macd: Macd::new(macd_fast, macd_slow, macd_signal),
            prev_fast: None,
            prev_slow: None,
            in_position: false,
            fast_period,
            slow_period,
            macd_fast,
            macd_slow,
            macd_signal,
        }
    }
}

impl Strategy for TrendFollower {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast_val = self.fast_ma.update(bar.close);
        let slow_val = self.slow_ma.update(bar.close);
        let macd_val = self.macd.update(bar.close);

        let (Some(f), Some(s), Some(m)) = (fast_val, slow_val, macd_val) else {
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

        if crossed_above && m.histogram > 0.0 && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (crossed_below || m.histogram < 0.0) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "trend_follower"
    }

    fn reset(&mut self) {
        self.fast_ma = Sma::new(self.fast_period);
        self.slow_ma = Sma::new(self.slow_period);
        self.macd = Macd::new(self.macd_fast, self.macd_slow, self.macd_signal);
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
    fn trend_follower_parity() {
        let bars = slow_trend_bars();

        let mut hc = TrendFollower::new(50, 200, 12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "sma", "period": 50 },
                "slow": { "type": "sma", "period": 200 },
                "macd": { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "cross_above",
                  "compare": "slow", "compare_field": "value" },
                { "source": "macd", "field": "histogram", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "fast", "field": "value", "op": "cross_below",
                  "compare": "slow", "compare_field": "value" },
                { "source": "macd", "field": "histogram", "op": "lt", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_sma(50) <= prev_sma(200) && sma(50) > sma(200) && macd_hist(12) > 0.0",
            "exit":  "(prev_sma(50) >= prev_sma(200) && sma(50) < sma(200)) || macd_hist(12) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "trend_follower: no signals");
        assert_parity("trend_follower hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("trend_follower hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
