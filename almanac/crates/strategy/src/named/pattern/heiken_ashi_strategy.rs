use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, HeikenAshi};

/// Bot — Heiken Ashi Color.
///
/// Long when HA candle flips to bullish (red → green).
/// Close when HA candle flips to bearish.
pub struct HaColor {
    ha: HeikenAshi,
    smooth: usize,
    prev_bullish: Option<bool>,
}

impl HaColor {
    /// `smooth = 1` = standard HA, no EMA smoothing.
    pub fn new(smooth: usize) -> Self {
        Self {
            ha: HeikenAshi::new(smooth),
            smooth,
            prev_bullish: None,
        }
    }
}

impl Strategy for HaColor {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.ha.update(bar.open, bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let prev = self.prev_bullish.replace(v.is_bullish);
        let Some(was_bullish) = prev else {
            return vec![];
        };

        if v.is_bullish && !was_bullish {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.is_bullish && was_bullish {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "heiken_ashi_color"
    }

    fn reset(&mut self) {
        self.ha = HeikenAshi::new(self.smooth);
        self.prev_bullish = None;
    }
}

/// Bot — Heiken Ashi Breakout.
///
/// Long after `consecutive_bars` consecutive bullish HA candles.
/// Close after `consecutive_bars` consecutive bearish HA candles.
pub struct HaBreakout {
    ha: HeikenAshi,
    smooth: usize,
    consecutive_bars: usize,
    bull_count: usize,
    bear_count: usize,
}

impl HaBreakout {
    pub fn new(smooth: usize, consecutive_bars: usize) -> Self {
        Self {
            ha: HeikenAshi::new(smooth),
            smooth,
            consecutive_bars,
            bull_count: 0,
            bear_count: 0,
        }
    }
}

impl Strategy for HaBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.ha.update(bar.open, bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if v.is_bullish {
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
}

/// Bot — Heiken Ashi Harmonizer.
///
/// Long when HA is bullish AND close > EMA(period).
/// Close when HA turns bearish OR close < EMA.
pub struct HaHarmonizer {
    ha: HeikenAshi,
    ema: Ema,
    smooth: usize,
    ema_period: usize,
}

impl HaHarmonizer {
    pub fn new(smooth: usize, ema_period: usize) -> Self {
        Self {
            ha: HeikenAshi::new(smooth),
            ema: Ema::new(ema_period),
            smooth,
            ema_period,
        }
    }
}

impl Strategy for HaHarmonizer {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let ha = self.ha.update(bar.open, bar.high, bar.low, bar.close);
        let Some(ema) = self.ema.update(bar.close) else {
            return vec![];
        };
        let Some(ha) = ha else {
            return vec![];
        };

        let bullish_setup = ha.is_bullish && bar.close > ema;

        if bullish_setup {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !ha.is_bullish || bar.close < ema {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "heiken_ashi_harmonizer"
    }

    fn reset(&mut self) {
        self.ha = HeikenAshi::new(self.smooth);
        self.ema = Ema::new(self.ema_period);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn script_parity_ha_color() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = HaColor::new(1);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
candle.transform("heiken_ashi");
let is_bullish = close[0] >= open[0];
let was_bullish = close[1] >= open[1];
if is_bullish && !was_bullish { entry = true; }
if !is_bullish && was_bullish { exit = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "heiken_ashi_color: must produce signals");
        assert_parity("heiken_ashi_color parity vs named", &named_sigs, &script_sigs);
    }

    #[test]
    fn script_parity_ha_breakout() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = HaBreakout::new(1, 2);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
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
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "heiken_ashi_breakout: must produce signals");
        assert_parity("heiken_ashi_breakout parity vs named", &named_sigs, &script_sigs);
    }
}
