use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Rsi;

/// RSI Mean Reversion strategy.
/// Buy when RSI drops below `oversold`; sell/close when RSI rises above `overbought`.
pub struct RsiMeanRev {
    rsi: Rsi,
    oversold: f64,
    overbought: f64,
    period: usize,
}

impl RsiMeanRev {
    pub fn new(period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            rsi: Rsi::new(period),
            oversold,
            overbought,
            period,
        }
    }
}

impl Strategy for RsiMeanRev {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(rsi) = self.rsi.update(bar.close) else {
            return vec![];
        };

        if rsi < self.oversold {
            let strength = (self.oversold - rsi) / self.oversold;
            return vec![Signal::long(
                bar.timestamp,
                &bar.symbol,
                strength.clamp(0.0, 1.0),
            )];
        }

        if rsi > self.overbought {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "rsi_mean_rev"
    }

    fn reset(&mut self) {
        self.rsi = Rsi::new(self.period);
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let rsi14 = ind.rsi(14, buf=1);
if rsi14[0] < 30.0 { entry = true; }
if rsi14[0] > 70.0 { exit  = true; }
"#;
#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn test_no_signal_before_rsi_ready() {
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = RsiMeanRev::new(14, 30.0, 70.0);
        for i in 0..14 {
            let s = strat.on_bar(&bars[i]);
            assert!(s.is_empty(), "no signal before RSI ready");
        }
    }

    #[test]
    fn test_long_signal_on_oversold() {
        use alm_core::signal::Direction;
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = RsiMeanRev::new(14, 30.0, 70.0);
        let mut signals = Vec::new();
        for b in &bars {
            signals.extend(strat.on_bar(b));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit Long when RSI oversold"
        );
    }

    #[test]
    fn test_close_signal_on_overbought() {
        use alm_core::signal::Direction;
        let Some(bars) = load_real_bars() else { return; };
        let mut strat = RsiMeanRev::new(14, 30.0, 70.0);
        let mut signals = Vec::new();
        for b in &bars {
            signals.extend(strat.on_bar(b));
        }
        assert!(
            signals.iter().any(|s| s.direction == Direction::Exit),
            "should emit Close when RSI overbought"
        );
    }

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = RsiMeanRev::new(14, 30.0, 70.0);
        let named_sigs = run(&mut named, &bars);

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "rsi_mean_rev: must produce signals");
        assert_parity("rsi_mean_rev parity vs named", &named_sigs, &script_sigs);
    }
}
