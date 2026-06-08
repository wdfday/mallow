use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Atr, Sma};

/// Bot #37 — Volatility Squeezer.
///
/// Long when ATR is expanding (current ATR > previous ATR, volatility increasing)
/// AND price is above MA (trade in the direction of the trend).
/// Closes when price falls below MA.
pub struct VolatilitySqueezer {
    atr: Atr,
    ma: Sma,
    prev_atr: Option<f64>,
    atr_period: usize,
    ma_period: usize,
}

impl VolatilitySqueezer {
    pub fn new(atr_period: usize, ma_period: usize) -> Self {
        Self {
            atr: Atr::new(atr_period),
            ma: Sma::new(ma_period),
            prev_atr: None,
            atr_period,
            ma_period,
        }
    }
}

impl Strategy for VolatilitySqueezer {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let atr_val = self.atr.update(bar.high, bar.low, bar.close);
        let ma_val = self.ma.update(bar.close);

        let (Some(atr), Some(ma)) = (atr_val, ma_val) else {
            return vec![];
        };

        let Some(prev_atr) = self.prev_atr else {
            self.prev_atr = Some(atr.atr);
            return vec![];
        };

        let atr_expanding = atr.atr > prev_atr;
        self.prev_atr = Some(atr.atr);

        if atr_expanding && bar.close > ma {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < ma {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "volatility_squeezer"
    }

    fn reset(&mut self) {
        self.atr = Atr::new(self.atr_period);
        self.ma = Sma::new(self.ma_period);
        self.prev_atr = None;
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

        let mut named = VolatilitySqueezer::new(14, 50);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
let atr14 = ind.atr(14);
let sma50 = ind.sma(50);
let atr_expanding = atr14[0].atr > atr14[1].atr;
if atr_expanding && close[0] > sma50[0] {
    entry = true;
}
if close[0] < sma50[0] {
    exit = true;
}
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "volatility_squeezer: must produce signals");
        assert_parity("volatility_squeezer parity vs named", &named_sigs, &script_sigs);
    }
}
