use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, WilliamsR};

const RHAI: &str = r#"
let wr14  = ind.williams_r(14);
let ema50 = ind.ema(50, 1);
if wr14[1] <= -80.0 && wr14[0] > -80.0 && close[0] > ema50[0] { entry = true; }
if (wr14[1] >= -20.0 && wr14[0] < -20.0) || close[0] < ema50[0] { exit  = true; }
"#;

/// Williams %R + EMA trend filter.
///
/// Long when %R exits oversold (crosses above -80) while price is above the trend EMA.
/// Close when %R enters overbought (crosses below -20) or price falls below EMA.
pub struct WilliamsRMa {
    wr: WilliamsR,
    ema: Ema,
    prev_wr: Option<f64>,
    wr_period: usize,
    ema_period: usize,
    oversold: f64,
    overbought: f64,
}

impl WilliamsRMa {
    pub fn new(wr_period: usize, ema_period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            wr: WilliamsR::new(wr_period),
            ema: Ema::new(ema_period),
            prev_wr: None,
            wr_period,
            ema_period,
            oversold,
            overbought,
        }
    }
}

impl Strategy for WilliamsRMa {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let wr = self.wr.update(bar.high, bar.low, bar.close);
        let ema = self.ema.update(bar.close);

        let (Some(wr), Some(ema)) = (wr, ema) else {
            return vec![];
        };

        let Some(prev_wr) = self.prev_wr else {
            self.prev_wr = Some(wr);
            return vec![];
        };

        let exited_oversold = prev_wr <= self.oversold && wr > self.oversold;
        let entered_overbought = prev_wr >= self.overbought && wr < self.overbought;
        self.prev_wr = Some(wr);

        if exited_oversold && bar.close > ema {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if entered_overbought || bar.close < ema {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "williams_r_ma"
    }

    fn description(&self) -> &'static str {
        "Long when Williams %R exits oversold and close is above EMA. Exit when overbought or price drops below EMA."
    }

    fn script(&self) -> Option<&'static str> { Some(RHAI) }

    fn reset(&mut self) {
        self.wr = WilliamsR::new(self.wr_period);
        self.ema = Ema::new(self.ema_period);
        self.prev_wr = None;
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
        let bars = dip_in_uptrend_bars();

        let mut named = WilliamsRMa::new(14, 50, -80.0, -20.0);
        let named_sigs = run(&mut named, &bars);

        // williams is Single; ema is Single
        let script = WilliamsRMa::new(14, 50, -80.0, -20.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "williams_r_ma: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}