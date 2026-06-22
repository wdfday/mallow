use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Adx;

pub(crate) const RHAI: &str = r#"
let adx14 = ind.adx(14);
let dmi14 = ind.dmi(14);
if dmi14[1].plus_di <= dmi14[1].minus_di && dmi14[0].plus_di > dmi14[0].minus_di && adx14[0] > 25.0 { entry = true; }
if dmi14[1].plus_di >= dmi14[1].minus_di && dmi14[0].plus_di < dmi14[0].minus_di { exit  = true; }
"#;

/// Bot #14 — DMI and ADX.
///
/// Long when ADX > threshold AND +DI crosses above -DI.
/// Closes when -DI crosses above +DI.
pub struct DmiAdx {
    adx: Adx,
    prev_plus_di: Option<f64>,
    prev_minus_di: Option<f64>,
    adx_threshold: f64,
    period: usize,
}

impl DmiAdx {
    pub fn new(period: usize, adx_threshold: f64) -> Self {
        Self {
            adx: Adx::new(period),
            prev_plus_di: None,
            prev_minus_di: None,
            adx_threshold,
            period,
        }
    }
}

impl Strategy for DmiAdx {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.adx.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let (Some(pp), Some(pm)) = (self.prev_plus_di, self.prev_minus_di) else {
            self.prev_plus_di = Some(v.plus_di);
            self.prev_minus_di = Some(v.minus_di);
            return vec![];
        };

        let plus_crossed_above = pp <= pm && v.plus_di > v.minus_di;
        let minus_crossed_above = pm <= pp && v.minus_di > v.plus_di;

        self.prev_plus_di = Some(v.plus_di);
        self.prev_minus_di = Some(v.minus_di);

        if plus_crossed_above && v.adx > self.adx_threshold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, v.adx / 100.0)];
        }
        if minus_crossed_above {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "dmi_adx"
    }

    fn description(&self) -> &'static str {
        "Long when ADX > threshold and +DI crosses above -DI. Exit when -DI crosses above +DI."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI)
    }

    fn reset(&mut self) {
        self.adx = Adx::new(self.period);
        self.prev_plus_di = None;
        self.prev_minus_di = None;
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

        let mut named = DmiAdx::new(14, 25.0);
        let named_sigs = run(&mut named, &bars);

        // ADX Multi: .adx .plus_di .minus_di; default buf_depth=2
        let script = DmiAdx::new(14, 25.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "dmi_adx: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}

