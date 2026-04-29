use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Cmo, Ema};

/// Chande Momentum Oscillator zero-line cross + EMA trend filter.
///
/// Long when CMO crosses above zero (positive momentum) and price > EMA.
/// Close when CMO crosses below zero or price falls below EMA.
pub struct CmoZeroCross {
    cmo: Cmo,
    ema: Ema,
    prev_cmo: Option<f64>,
    in_position: bool,
    cmo_period: usize,
    ema_period: usize,
}

impl CmoZeroCross {
    pub fn new(cmo_period: usize, ema_period: usize) -> Self {
        Self {
            cmo: Cmo::new(cmo_period),
            ema: Ema::new(ema_period),
            prev_cmo: None,
            in_position: false,
            cmo_period,
            ema_period,
        }
    }
}

impl Strategy for CmoZeroCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let cmo = self.cmo.update(bar.close);
        let ema = self.ema.update(bar.close);

        let (Some(cmo), Some(ema)) = (cmo, ema) else {
            return vec![];
        };

        let Some(prev) = self.prev_cmo else {
            self.prev_cmo = Some(cmo);
            return vec![];
        };

        let crossed_above_zero = prev <= 0.0 && cmo > 0.0;
        let crossed_below_zero = prev >= 0.0 && cmo < 0.0;
        self.prev_cmo = Some(cmo);

        if crossed_above_zero && bar.close > ema && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (crossed_below_zero || bar.close < ema) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "cmo_zero_cross"
    }

    fn reset(&mut self) {
        self.cmo = Cmo::new(self.cmo_period);
        self.ema = Ema::new(self.ema_period);
        self.prev_cmo = None;
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
    fn cmo_zero_cross_parity() {
        let bars = dip_in_uptrend_bars();

        let mut hc = CmoZeroCross::new(14, 50);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "cmo": { "type": "cmo", "period": 14 },
                "ema": { "type": "ema", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "cmo",   "field": "value", "op": "cross_above", "value": 0.0 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ema" }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "cmo",   "field": "value", "op": "cross_below", "value": 0.0 },
                { "source": "close", "field": "value", "op": "lt", "compare": "ema" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_cmo(14) <= 0.0 && cmo(14) > 0.0 && close > ema(50)",
            "exit":  "(prev_cmo(14) >= 0.0 && cmo(14) < 0.0) || close < ema(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "cmo_zero_cross: no signals");
        assert_parity("cmo_zero_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("cmo_zero_cross hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
