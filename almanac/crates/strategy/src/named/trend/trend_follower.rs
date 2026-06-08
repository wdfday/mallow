use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Macd, Sma};

/// Bot #33 — Trend Follower.
///
/// Long when fast SMA > slow SMA AND MACD histogram > 0 (state-based, with position guard).
/// Exits when SMA inverts OR histogram turns negative.
pub struct TrendFollower {
    fast_ma: Sma,
    slow_ma: Sma,
    macd: Macd,
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

        if !self.in_position && f > s && m.histogram > 0.0 {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (f < s || m.histogram < 0.0) {
            self.in_position = false;
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "trend_follower"
    }

    fn description(&self) -> &'static str {
        "Long when fast SMA > slow SMA AND MACD histogram > 0. Exit when SMA inverts or histogram turns negative."
    }

    fn reset(&mut self) {
        self.fast_ma = Sma::new(self.fast_period);
        self.slow_ma = Sma::new(self.slow_period);
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
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = TrendFollower::new(50, 200, 12, 26, 9);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
let s50  = ind.sma(50, buf=1);
let s200 = ind.sma(200, buf=1);
let m    = ind.macd(12, buf=1);
let in_pos = state["in_position"] == true;
if !in_pos && s50[0] > s200[0] && m[0].histogram > 0.0 {
    entry = true;
    state["in_position"] = true;
}
if in_pos && (s50[0] < s200[0] || m[0].histogram < 0.0) {
    exit = true;
    state["in_position"] = false;
}
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        // assert!(!named_sigs.is_empty(), "trend_follower: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
