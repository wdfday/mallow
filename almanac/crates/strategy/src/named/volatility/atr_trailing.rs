use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Atr, Ema};

/// ATR Trailing Stop strategy.
///
/// Uses EMA for trend direction + ATR for dynamic stop-loss placement.
/// Enter long when price > EMA (uptrend).
/// Exit when price drops below entry - ATR * multiplier (trailing stop hit).
pub struct AtrTrailingStop {
    ema: Ema,
    atr: Atr,
    ema_period: usize,
    atr_period: usize,
    atr_multiplier: f64,
    in_position: bool,
    trailing_stop: f64,
    highest_since_entry: f64,
}

impl AtrTrailingStop {
    pub fn new(ema_period: usize, atr_period: usize, atr_multiplier: f64) -> Self {
        Self {
            ema: Ema::new(ema_period),
            atr: Atr::new(atr_period),
            ema_period,
            atr_period,
            atr_multiplier,
            in_position: false,
            trailing_stop: 0.0,
            highest_since_entry: 0.0,
        }
    }

    /// Standard: EMA(20), ATR(14), multiplier 2.0
    pub fn standard() -> Self {
        Self::new(20, 14, 2.0)
    }
}

impl Strategy for AtrTrailingStop {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let ema_val = self.ema.update(bar.close);
        let atr_val = self.atr.update(bar.high, bar.low, bar.close);

        let (Some(ema), Some(atr)) = (ema_val, atr_val) else {
            return vec![];
        };

        let atr_value = atr.atr;

        if self.in_position {
            // Update trailing stop
            if bar.close > self.highest_since_entry {
                self.highest_since_entry = bar.close;
                self.trailing_stop = self.highest_since_entry - atr_value * self.atr_multiplier;
            }

            // Check stop hit
            if bar.close < self.trailing_stop {
                self.in_position = false;
                self.trailing_stop = 0.0;
                self.highest_since_entry = 0.0;
                return vec![Signal::exit(bar.timestamp, &bar.symbol)];
            }
        } else {
            // Enter long when price above EMA (uptrend)
            if bar.close > ema {
                self.in_position = true;
                self.highest_since_entry = bar.close;
                self.trailing_stop = bar.close - atr_value * self.atr_multiplier;
                return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
            }
        }

        vec![]
    }

    fn name(&self) -> &str {
        "atr_trailing_stop"
    }

    fn reset(&mut self) {
        self.ema = Ema::new(self.ema_period);
        self.atr = Atr::new(self.atr_period);
        self.in_position = false;
        self.trailing_stop = 0.0;
        self.highest_since_entry = 0.0;
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

        let mut named = AtrTrailingStop::new(20, 14, 2.0);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
let ema20 = ind.ema(20, buf=1);
let atr14 = ind.atr(14, buf=1);
if state["in_position"] == () {
    state["in_position"] = false;
    state["highest_since_entry"] = 0.0;
    state["trailing_stop"] = 0.0;
}
if state["in_position"] {
    if close[0] > state["highest_since_entry"] {
        state["highest_since_entry"] = close[0];
        state["trailing_stop"] = close[0] - atr14[0].atr * 2.0;
    }
    if close[0] < state["trailing_stop"] {
        state["in_position"] = false;
        state["highest_since_entry"] = 0.0;
        state["trailing_stop"] = 0.0;
        exit = true;
    }
} else {
    if close[0] > ema20[0] {
        state["in_position"] = true;
        state["highest_since_entry"] = close[0];
        state["trailing_stop"] = close[0] - atr14[0].atr * 2.0;
        entry = true;
    }
}
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "atr_trailing: must produce signals");
        assert_parity("atr_trailing parity vs named", &named_sigs, &script_sigs);
    }
}
