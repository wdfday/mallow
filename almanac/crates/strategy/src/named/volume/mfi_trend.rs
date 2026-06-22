use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Mfi;

pub(crate) const RHAI_SCRIPT: &str = r#"
let mfi14 = ind.mfi(14);
if mfi14[1] <= 50.0 && mfi14[0] > 50.0 { entry = true; }
if mfi14[0] < 40.0  { exit  = true; }
"#;

/// Bot — MFI Trend.
///
/// Long when MFI crosses above `bull_threshold` (default 50) — money is flowing in.
/// Close when MFI drops below `bear_threshold` (default 40).
pub struct MfiTrend {
    mfi: Mfi,
    bull_threshold: f64,
    bear_threshold: f64,
    prev_mfi: Option<f64>,
    period: usize,
}

impl MfiTrend {
    pub fn new(period: usize, bull_threshold: f64, bear_threshold: f64) -> Self {
        Self {
            mfi: Mfi::new(period),
            bull_threshold,
            bear_threshold,
            prev_mfi: None,
            period,
        }
    }
}

impl Strategy for MfiTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.mfi.update(bar.high, bar.low, bar.close, bar.volume) else {
            return vec![];
        };

        let prev = self.prev_mfi.replace(v);
        let Some(p) = prev else {
            return vec![];
        };

        if p <= self.bull_threshold && v > self.bull_threshold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if v < self.bear_threshold {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "mfi_trend"
    }

    fn description(&self) -> &'static str {
        "Long when MFI crosses above bull threshold (money flowing in). Exit when MFI drops below bear threshold."
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
    fn mfi_trend_script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = MfiTrend::new(14, 50.0, 40.0);
        let named_sigs = run(&mut named, &bars);

        let script = MfiTrend::new(14, 50.0, 40.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "mfi_trend: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
