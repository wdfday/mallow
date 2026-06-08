use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Atr;
use std::collections::VecDeque;

/// Chandelier Exit — ATR-based trailing stop system.
///
/// Bullish when close > (rolling-highest-high - multiplier * ATR).
/// Long on transition from non-bullish to bullish.
/// Exit on transition from bullish to non-bullish.
///
/// Default: period=22, multiplier=3.0
pub struct ChandelierExit {
    atr: Atr,
    period: usize,
    multiplier: f64,
    atr_p: usize,
    highs: VecDeque<f64>,
    in_position: bool,
    prev_bull: Option<bool>,
}

impl ChandelierExit {
    pub fn new(period: usize, multiplier: f64) -> Self {
        Self {
            atr: Atr::new(period),
            period,
            multiplier,
            atr_p: period,
            highs: VecDeque::with_capacity(period),
            in_position: false,
            prev_bull: None,
        }
    }
}

impl Strategy for ChandelierExit {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        self.highs.push_back(bar.high);
        if self.highs.len() > self.period { self.highs.pop_front(); }

        let Some(atr_val) = self.atr.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };
        if self.highs.len() < self.period { return vec![]; }

        let hh = self.highs.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let stop = hh - self.multiplier * atr_val.atr;
        let bull = bar.close > stop;

        let was_bull = self.prev_bull.replace(bull);
        let Some(prev) = was_bull else {
            return vec![];
        };

        if !prev && bull && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if prev && !bull && self.in_position {
            self.in_position = false;
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "chandelier_exit"
    }

    fn description(&self) -> &'static str {
        "Long when close crosses above chandelier stop (HH - mult*ATR). Exit when close drops below it."
    }

    fn reset(&mut self) {
        self.atr = Atr::new(self.atr_p);
        self.highs.clear();
        self.in_position = false;
        self.prev_bull = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = ChandelierExit::new(22, 3.0);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
let atr22 = ind.atr(22);
let hh = highest(high, 22);
let stop = hh - 3.0 * atr22[0].atr;
let bull = close[0] > stop;
if state["in_position"] == () {
    state["in_position"] = false;
    state["prev_bull"] = ();
}
let was_bull = state["prev_bull"];
state["prev_bull"] = bull;
if was_bull != () {
    if !was_bull && bull && !state["in_position"] {
        state["in_position"] = true;
        entry = true;
    }
    if was_bull && !bull && state["in_position"] {
        state["in_position"] = false;
        exit = true;
    }
}
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "chandelier_exit: must produce signals");
        assert_parity("chandelier_exit parity vs named", &named_sigs, &script_sigs);
    }
}
