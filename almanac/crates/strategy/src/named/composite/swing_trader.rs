use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Adx, Cci};

const RHAI: &str = r#"
let cci20 = ind.cci(20);
let adx14 = ind.adx(14, buf=1);
if cci20[1] <= 100.0 && cci20[0] > 100.0 && adx14[0] > 25.0 { entry = true; }
if cci20[1] >= -100.0 && cci20[0] < -100.0 { exit  = true; }
"#;

/// Bot #32 — Swing Trader.
///
/// Long when CCI breaks above +100 (strong upside momentum) AND ADX > threshold
/// (confirms the move is part of a trending environment, not just noise).
/// Closes when CCI crosses back below −100.
pub struct SwingTrader {
    cci: Cci,
    adx: Adx,
    prev_cci: Option<f64>,
    adx_threshold: f64,
    cci_period: usize,
    adx_period: usize,
}

impl SwingTrader {
    pub fn new(cci_period: usize, adx_period: usize, adx_threshold: f64) -> Self {
        Self {
            cci: Cci::new(cci_period),
            adx: Adx::new(adx_period),
            prev_cci: None,
            adx_threshold,
            cci_period,
            adx_period,
        }
    }
}

impl Strategy for SwingTrader {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let cci_val = self.cci.update(bar.high, bar.low, bar.close);
        let adx_val = self.adx.update(bar.high, bar.low, bar.close);

        let (Some(cci), Some(adx)) = (cci_val, adx_val) else {
            return vec![];
        };

        let Some(prev) = self.prev_cci else {
            self.prev_cci = Some(cci);
            return vec![];
        };

        let cci_crossed_above_100 = prev <= 100.0 && cci > 100.0;
        let cci_crossed_below_minus100 = prev >= -100.0 && cci < -100.0;

        self.prev_cci = Some(cci);

        if cci_crossed_above_100 && adx.adx > self.adx_threshold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if cci_crossed_below_minus100 {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "swing_trader"
    }

    fn description(&self) -> &'static str {
        "Long when CCI breaks above +100 with ADX confirming a trend. Exit when CCI drops below -100."
    }

    fn script(&self) -> Option<&'static str> { Some(RHAI) }

    fn reset(&mut self) {
        self.cci = Cci::new(self.cci_period);
        self.adx = Adx::new(self.adx_period);
        self.prev_cci = None;
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

        let mut named = SwingTrader::new(20, 14, 25.0);
        let named_sigs = run(&mut named, &bars);

        // CCI is Single, ADX is Multi
        let script = SwingTrader::new(20, 14, 25.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "swing_trader: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
