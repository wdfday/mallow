use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Mfi;

pub(crate) const RHAI_SCRIPT: &str = r#"
let mfi14 = ind.mfi(14);
if mfi14[1] <= 20.0 && mfi14[0] > 20.0 { entry = true; }
if mfi14[1] <= 80.0 && mfi14[0] > 80.0 { exit  = true; }
"#;

/// Bot — MFI Mean Reversion.
///
/// Long when MFI crosses above `oversold` (default 20) — oversold recovery.
/// Close when MFI crosses above `overbought` (default 80).
pub struct MfiRevert {
    mfi: Mfi,
    oversold: f64,
    overbought: f64,
    prev_mfi: Option<f64>,
    period: usize,
}

impl MfiRevert {
    pub fn new(period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            mfi: Mfi::new(period),
            oversold,
            overbought,
            prev_mfi: None,
            period,
        }
    }
}

impl Strategy for MfiRevert {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.mfi.update(bar.high, bar.low, bar.close, bar.volume) else {
            return vec![];
        };

        let prev = self.prev_mfi.replace(v);
        let Some(p) = prev else {
            return vec![];
        };

        if p <= self.oversold && v > self.oversold  {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if p <= self.overbought && v > self.overbought  {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "mfi_revert"
    }

    fn description(&self) -> &'static str {
        "Long when MFI crosses above oversold level. Exit when MFI crosses into overbought territory."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn reset(&mut self) {
        self.mfi = Mfi::new(self.period);
        self.prev_mfi = None;
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
    fn mfi_revert_script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = MfiRevert::new(14, 20.0, 80.0);
        let named_sigs = run(&mut named, &bars);

        let script = MfiRevert::new(14, 20.0, 80.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "mfi_revert: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
