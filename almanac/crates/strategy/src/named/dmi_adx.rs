use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Adx;

/// Bot #14 — DMI and ADX.
///
/// Long when ADX > threshold AND +DI crosses above -DI.
/// Closes when -DI crosses above +DI.
pub struct DmiAdx {
    adx: Adx,
    prev_plus_di: Option<f64>,
    prev_minus_di: Option<f64>,
    adx_threshold: f64,
    in_position: bool,
    period: usize,
}

impl DmiAdx {
    pub fn new(period: usize, adx_threshold: f64) -> Self {
        Self {
            adx: Adx::new(period),
            prev_plus_di: None,
            prev_minus_di: None,
            adx_threshold,
            in_position: false,
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

        if plus_crossed_above && v.adx > self.adx_threshold && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, v.adx / 100.0)];
        }
        if minus_crossed_above && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "dmi_adx"
    }

    fn reset(&mut self) {
        self.adx = Adx::new(self.period);
        self.prev_plus_di = None;
        self.prev_minus_di = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn dmi_adx_parity() {
        let bars = trending_bars(300);

        // 1. hardcoded (period=14, adx_threshold=25.0)
        let mut hc = DmiAdx::new(14, 25.0);
        let hc_sigs = run(&mut hc, &bars);

        // 2. dynamic JSON — two-rule entry: +DI cross_above −DI AND adx > 25
        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "adx": { "type": "adx", "period": 14 } },
            "entry": {
                "logic": "and",
                "rules": [
                    { "source": "adx", "field": "plus_di",  "op": "cross_above", "compare": "adx", "compare_field": "minus_di" },
                    { "source": "adx", "field": "adx",      "op": "gt",          "value": 25.0 }
                ]
            },
            "exit": {
                "logic": "and",
                "rules": [
                    { "source": "adx", "field": "minus_di", "op": "cross_above", "compare": "adx", "compare_field": "plus_di" }
                ]
            }
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        // 3. CEL
        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_plus_di(14) <= prev_minus_di(14) && plus_di(14) > minus_di(14) && adx(14) > 25.0",
            "exit":  "prev_minus_di(14) <= prev_plus_di(14) && minus_di(14) > plus_di(14)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "dmi_adx: hardcoded produced no signals");
        assert_parity("dmi_adx hardcoded vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("dmi_adx hardcoded vs cel",     &hc_sigs, &cel_sigs);
    }
}
