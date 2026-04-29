use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Cmf, Ema};

/// Chaikin Money Flow + EMA trend filter.
///
/// Long when CMF > bull_threshold (strong buying pressure) and close > EMA.
/// Close when CMF < -bear_threshold (selling pressure) or close < EMA.
pub struct CmfEmaTrend {
    cmf: Cmf,
    ema: Ema,
    in_position: bool,
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
            in_position: false,
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

        if cmf > self.bull_threshold && bar.close > ema && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (cmf < -self.bear_threshold || bar.close < ema) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "cmf_ema_trend"
    }

    fn reset(&mut self) {
        self.cmf = Cmf::new(self.cmf_period);
        self.ema = Ema::new(self.ema_period);
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
    fn cmf_ema_trend_parity() {
        let bars = cmf_bars();

        let mut hc = CmfEmaTrend::new(20, 50, 0.1, 0.1);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "cmf": { "type": "cmf", "period": 20 },
                "ema": { "type": "ema", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "cmf",   "field": "value", "op": "gt", "value": 0.1 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ema" }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "cmf",   "field": "value", "op": "lt", "value": -0.1 },
                { "source": "close", "field": "value", "op": "lt", "compare": "ema" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "cmf(20) > 0.1 && close > ema(50)",
            "exit":  "cmf(20) < -0.1 || close < ema(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "cmf_ema_trend: no signals");
        assert_parity("cmf_ema_trend hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("cmf_ema_trend hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
