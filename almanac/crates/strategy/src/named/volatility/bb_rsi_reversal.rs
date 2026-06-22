use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{BBands, Rsi};

const RHAI: &str = r#"
let bb20  = ind.bbands(20, buf=1);
let rsi14 = ind.rsi(14, buf=1);
if close[0] < bb20[0].lower && rsi14[0] < 35.0 { entry = true; }
if close[0] > bb20[0].middle || rsi14[0] > 65.0 { exit  = true; }
"#;

/// Bollinger Band lower-touch + RSI oversold double confirmation.
///
/// Long when price closes below the lower band AND RSI < oversold threshold.
/// Close when price recovers above the middle band OR RSI > overbought threshold.
pub struct BbRsiReversal {
    bb: BBands,
    rsi: Rsi,
    bb_period: usize,
    bb_std: f64,
    rsi_period: usize,
    oversold: f64,
    overbought: f64,
}

impl BbRsiReversal {
    pub fn new(
        bb_period: usize,
        bb_std: f64,
        rsi_period: usize,
        oversold: f64,
        overbought: f64,
    ) -> Self {
        Self {
            bb: BBands::new(bb_period, bb_std),
            rsi: Rsi::new(rsi_period),
            bb_period,
            bb_std,
            rsi_period,
            oversold,
            overbought,
        }
    }
}

impl Strategy for BbRsiReversal {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let bb = self.bb.update(bar.close);
        let rsi = self.rsi.update(bar.close);

        let (Some(bb), Some(rsi)) = (bb, rsi) else {
            return vec![];
        };

        if bar.close < bb.lower && rsi < self.oversold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close > bb.middle || rsi > self.overbought {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "bb_rsi_reversal"
    }

    fn description(&self) -> &'static str {
        "Long when price closes below lower Bollinger Band AND RSI < oversold. Exit when price recovers above middle band or RSI > overbought."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.rsi = Rsi::new(self.rsi_period);
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let bb20  = ind.bbands(20, buf=1);
let rsi14 = ind.rsi(14, buf=1);
if close[0] < bb20[0].lower && rsi14[0] < 35.0 { entry = true; }
if close[0] > bb20[0].middle || rsi14[0] > 65.0 { exit  = true; }
"#;
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

        let mut named = BbRsiReversal::new(20, 2.0, 14, 35.0, 65.0);
        let named_sigs = run(&mut named, &bars);

        // bbands (not bb) is the script type name; both conditions fire every bar
        let script = BbRsiReversal::new(20, 2.0, 14, 35.0, 65.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "bb_rsi_reversal: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
