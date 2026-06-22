use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::HeikenAshi;

pub(crate) const RHAI_SCRIPT: &str = r#"
candle.transform("heiken_ashi");
if state["bull_count"] == () {
    state["bull_count"] = 0;
    state["bear_count"] = 0;
}
let is_bullish = close[0] >= open[0];
if is_bullish {
    state["bull_count"] = state["bull_count"] + 1;
    state["bear_count"] = 0;
} else {
    state["bear_count"] = state["bear_count"] + 1;
    state["bull_count"] = 0;
}
if state["bull_count"] >= 2 { entry = true; }
if state["bear_count"] >= 2 { exit = true; }
"#;

/// Bot — Heiken Ashi Consecutive Breakout.
///
/// Long after `consecutive_bars` bullish candles.
/// Close after `consecutive_bars` bearish candles.
pub struct HaBreakout {
    ha: HeikenAshi,
    bull_count: usize,
    bear_count: usize,
    smooth: usize,
    consecutive_bars: usize,
}

impl HaBreakout {
    pub fn new(smooth: usize, consecutive_bars: usize) -> Self {
        Self {
            ha: HeikenAshi::new(smooth),
            bull_count: 0,
            bear_count: 0,
            smooth,
            consecutive_bars,
        }
    }
}

impl Strategy for HaBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(ha) = self.ha.update(bar.open, bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if ha.is_bullish {
            self.bull_count += 1;
            self.bear_count = 0;
        } else {
            self.bear_count += 1;
            self.bull_count = 0;
        }

        if self.bull_count >= self.consecutive_bars {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.bear_count >= self.consecutive_bars {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "heiken_ashi_breakout"
    }

    fn reset(&mut self) {
        self.ha = HeikenAshi::new(self.smooth);
        self.bull_count = 0;
        self.bear_count = 0;
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
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
    fn script_parity_ha_breakout() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = HaBreakout::new(1, 2);
        let named_sigs = run(&mut named, &bars);

        let script = HaBreakout::new(1, 2).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "heiken_ashi_breakout: must produce signals");
        assert_parity("heiken_ashi_breakout parity vs named", &named_sigs, &script_sigs);
    }
}
