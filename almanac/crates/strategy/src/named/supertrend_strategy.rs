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
    use alm_core::signal::Direction;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    #[test]
    fn rhai_parity() {
        let bars = sar_bars();

        let mut named = SupertrendStrategy::new(10, 3.0);
        let named_sigs = run(&mut named, &bars);

        // st_bull returns 1.0 when bullish, 0.0 when bearish
        let script = r#"
let sb = ind.st_bull(10);
if sb[1] < 0.5 && sb[0] >= 0.5 { entry = true; }
if sb[1] >= 0.5 && sb[0] < 0.5 { exit  = true; }
"#;
        let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
        let rhai_sigs = run(rhai.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "supertrend: must produce signals");
        assert_eq!(named_sigs, rhai_sigs, "rhai parity failed");
    }
}
