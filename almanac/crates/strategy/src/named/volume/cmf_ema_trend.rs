use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Cmf, Ema};

pub(crate) const RHAI: &str = r#"
let cmf20 = ind.cmf(20, buf=1);
let ema50  = ind.ema(50, buf=1);
if cmf20[0] > 0.1 && close[0] > ema50[0] { entry = true; }
if cmf20[0] < -0.1 { exit = true; }
"#;

/// Chaikin Money Flow + EMA trend filter.
///
/// Long when CMF > bull_threshold (strong buying pressure) and close > EMA.
/// Close when CMF < -bear_threshold (selling pressure) or close < EMA.
pub struct CmfEmaTrend {
    cmf: Cmf,
    ema: Ema,
    cmf_period: usize,
    ema_period: usize,
    bull_threshold: f64,
    bear_threshold: f64,
}

impl CmfEmaTrend {
    pub fn new(
        cmf_period: usize,
        ema_period: usize,
        bull_threshold: f64,
        bear_threshold: f64,
    ) -> Self {
        Self {
            cmf: Cmf::new(cmf_period),
            ema: Ema::new(ema_period),
            cmf_period,
            ema_period,
            bull_threshold,
            bear_threshold,
        }
    }
}

impl Strategy for CmfEmaTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let cmf = self.cmf.update(bar.high, bar.low, bar.close, bar.volume);
        let ema = self.ema.update(bar.close);

        let (Some(cmf), Some(ema)) = (cmf, ema) else {
            return vec![];
        };

        if cmf > self.bull_threshold && bar.close > ema {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if cmf < -self.bear_threshold {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "cmf_ema_trend"
    }

    fn description(&self) -> &'static str {
        "Long when CMF > threshold and close > EMA. Exit when CMF turns negative."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI)
    }

    fn reset(&mut self) {
        self.cmf = Cmf::new(self.cmf_period);
        self.ema = Ema::new(self.ema_period);
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
        // CmfEmaTrend fires every bar (no in_position gate).
        let Some(bars) = load_real_bars() else { return; };

        let mut named = CmfEmaTrend::new(20, 50, 0.1, 0.1);
        let named_sigs = run(&mut named, &bars);

        // CMF is Single("value"); EMA is also Single
        let script = CmfEmaTrend::new(20, 50, 0.1, 0.1).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "cmf_ema_trend: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
