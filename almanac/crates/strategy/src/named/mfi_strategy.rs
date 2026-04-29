use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Mfi;

/// Bot — MFI Trend.
///
/// Long when MFI crosses above `bull_threshold` (default 50) — money is flowing in.
/// Close when MFI drops below `bear_threshold` (default 40).
pub struct MfiTrend {
    mfi: Mfi,
    bull_threshold: f64,
    bear_threshold: f64,
    prev_mfi: Option<f64>,
    in_position: bool,
    period: usize,
}

impl MfiTrend {
    pub fn new(period: usize, bull_threshold: f64, bear_threshold: f64) -> Self {
        Self {
            mfi: Mfi::new(period),
            bull_threshold,
            bear_threshold,
            prev_mfi: None,
            in_position: false,
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

        if p <= self.bull_threshold && v > self.bull_threshold && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if v < self.bear_threshold && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "mfi_trend"
    }

    fn reset(&mut self) {
        self.mfi = Mfi::new(self.period);
        self.prev_mfi = None;
        self.in_position = false;
    }
}

/// Bot — MFI Mean Reversion.
///
/// Long when MFI crosses above `oversold` (default 20) — oversold recovery.
/// Close when MFI crosses above `overbought` (default 80).
pub struct MfiRevert {
    mfi: Mfi,
    oversold: f64,
    overbought: f64,
    prev_mfi: Option<f64>,
    in_position: bool,
    period: usize,
}

impl MfiRevert {
    pub fn new(period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            mfi: Mfi::new(period),
            oversold,
            overbought,
            prev_mfi: None,
            in_position: false,
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

        if p <= self.oversold && v > self.oversold && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if p <= self.overbought && v > self.overbought && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "mfi_revert"
    }

    fn reset(&mut self) {
        self.mfi = Mfi::new(self.period);
        self.prev_mfi = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn mfi_trend_parity() {
        let bars = trending_bars(200);

        // 1. hardcoded (bull_threshold=50, bear_threshold=40)
        let mut hc = MfiTrend::new(14, 50.0, 40.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — entry: cross_above 50; exit: lt 40
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "mfi": { "type": "mfi", "period": 14 } },
            "entry": {
                "logic": "and",
                "rules": [{ "source": "mfi", "field": "value", "op": "cross_above", "value": 50.0 }]
            },
            "exit": {
                "logic": "and",
                "rules": [{ "source": "mfi", "field": "value", "op": "lt", "value": 40.0 }]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_mfi(14) <= 50.0 && mfi(14) > 50.0",
            "exit":  "mfi(14) < 40.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "mfi_trend: hardcoded produced no signals");
        assert_parity("mfi hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("mfi hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }
    */

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn mfi_revert_parity() {
        let bars = rsi_bars(200);

        let mut hc = MfiRevert::new(14, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "mfi": { "type": "mfi", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "mfi", "field": "value", "op": "cross_above", "value": 20.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "mfi", "field": "value", "op": "cross_above", "value": 80.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_mfi(14) <= 20.0 && mfi(14) > 20.0",
            "exit":  "prev_mfi(14) <= 80.0 && mfi(14) > 80.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "mfi_revert: no signals");
        assert_parity("mfi_revert hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("mfi_revert hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
