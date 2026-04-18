use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Macd, SuperTrend};

/// Bot — SuperTrend Trend Follower.
///
/// Long when SuperTrend is bullish (price above the band).
/// Close when SuperTrend flips bearish.
pub struct SupertrendStrategy {
    st: SuperTrend,
    period: usize,
    multiplier: f64,
    prev_bullish: Option<bool>,
    in_position: bool,
}

impl SupertrendStrategy {
    pub fn new(period: usize, multiplier: f64) -> Self {
        Self {
            st: SuperTrend::new(period, multiplier),
            period,
            multiplier,
            prev_bullish: None,
            in_position: false,
        }
    }
}

impl Strategy for SupertrendStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.st.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let prev = self.prev_bullish.replace(v.is_bullish);

        // Need a previous value to detect flip
        let Some(was_bullish) = prev else {
            return vec![];
        };

        if v.is_bullish && !was_bullish && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.is_bullish && was_bullish && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "supertrend"
    }

    fn reset(&mut self) {
        self.st = SuperTrend::new(self.period, self.multiplier);
        self.prev_bullish = None;
        self.in_position = false;
    }
}

/// Bot — SuperTrend + MACD confirmation.
///
/// Long when SuperTrend is bullish AND MACD histogram > 0.
/// Close when SuperTrend flips bearish.
pub struct SupertrendMacd {
    st: SuperTrend,
    macd: Macd,
    st_period: usize,
    multiplier: f64,
    macd_fast: usize,
    macd_slow: usize,
    macd_signal: usize,
    in_position: bool,
}

impl SupertrendMacd {
    pub fn new(
        st_period: usize,
        multiplier: f64,
        macd_fast: usize,
        macd_slow: usize,
        macd_signal: usize,
    ) -> Self {
        Self {
            st: SuperTrend::new(st_period, multiplier),
            macd: Macd::new(macd_fast, macd_slow, macd_signal),
            st_period,
            multiplier,
            macd_fast,
            macd_slow,
            macd_signal,
            in_position: false,
        }
    }
}

impl Strategy for SupertrendMacd {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let st = self.st.update(bar.high, bar.low, bar.close);
        let mc = self.macd.update(bar.close);

        let (Some(st), Some(mc)) = (st, mc) else {
            return vec![];
        };

        if st.is_bullish && mc.histogram > 0.0 && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !st.is_bullish && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "supertrend_macd"
    }

    fn reset(&mut self) {
        self.st = SuperTrend::new(self.st_period, self.multiplier);
        self.macd = Macd::new(self.macd_fast, self.macd_slow, self.macd_signal);
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
    fn supertrend_macd_parity() {
        let bars = trending_bars(300);

        let mut hc = SupertrendMacd::new(10, 3.0, 12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "st":   { "type": "supertrend", "period": 10, "multiplier": 3.0 },
                "macd": { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "st",   "field": "bullish",   "op": "gt", "value": 0.5 },
                { "source": "macd", "field": "histogram", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "st", "field": "bullish", "op": "lt", "value": 0.5 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "st_bull(10) >= 1.0 && macd_hist(12) > 0.0",
            "exit":  "st_bull(10) < 1.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "supertrend_macd: no signals");
        assert_parity("supertrend_macd hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("supertrend_macd hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn supertrend_dyn_cel_parity() {
        let bars = trending_bars(300);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "st": { "type": "supertrend", "period": 10, "multiplier": 3.0 } },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "st", "field": "bullish", "op": "cross_above", "value": 0.5 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "st", "field": "bullish", "op": "cross_below", "value": 0.5 }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_st_bull(10) < 1.0 && st_bull(10) >= 1.0",
            "exit":  "prev_st_bull(10) >= 1.0 && st_bull(10) < 1.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!dyn_sigs.is_empty(), "supertrend: dynamic produced no signals");
        assert_parity("supertrend dynamic vs cel", &dyn_sigs, &cel_sigs);
    }
}
