use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Macd, Sma};

const RHAI: &str = r#"
let s50  = ind.sma(50);
let s200 = ind.sma(200);
let m    = ind.macd(12);
if cross_above(s50, s200) && m[0].histogram > 0.0  { entry = true; }
if cross_below(s50, s200) || m[0].histogram < 0.0  { exit  = true; }
"#;

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

        if crossed_above && m.histogram > 0.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below || m.histogram < 0.0 {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "trend_follower"
    }

    fn description(&self) -> &'static str {
        "Long when fast SMA crosses above slow SMA with positive MACD histogram. Exit on SMA cross-down or negative histogram."
    }

    fn script(&self) -> Option<&'static str> { Some(RHAI) }

    fn reset(&mut self) {
        self.fast_ma = Sma::new(self.fast_period);
        self.slow_ma = Sma::new(self.slow_period);
        self.macd = Macd::new(self.macd_fast, self.macd_slow, self.macd_signal);
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
        let bars = slow_trend_bars();

        let mut named = TrendFollower::new(50, 200, 12, 26, 9);
        let named_sigs = run(&mut named, &bars);

        // SMA(50) / SMA(200) cross with MACD histogram confirmation/exit
        let script = TrendFollower::new(50, 200, 12, 26, 9).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "trend_follower: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
