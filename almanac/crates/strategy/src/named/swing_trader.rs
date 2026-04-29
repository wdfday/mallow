use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Adx, Cci};

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
    in_position: bool,
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
            in_position: false,
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

        if cci_crossed_above_100 && adx.adx > self.adx_threshold && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if cci_crossed_below_minus100 && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "swing_trader"
    }

    fn reset(&mut self) {
        self.cci = Cci::new(self.cci_period);
        self.adx = Adx::new(self.adx_period);
        self.prev_cci = None;
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
    fn swing_trader_parity() {
        let bars = trending_bars(300);

        let mut hc = SwingTrader::new(20, 14, 25.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "cci": { "type": "cci", "period": 20 },
                "adx": { "type": "adx", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "cci", "field": "value", "op": "cross_above", "value": 100.0 },
                { "source": "adx", "field": "adx",   "op": "gt", "value": 25.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "cci", "field": "value", "op": "cross_below", "value": -100.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_cci(20) <= 100.0 && cci(20) > 100.0 && adx(14) > 25.0",
            "exit":  "prev_cci(20) >= -100.0 && cci(20) < -100.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "swing_trader: no signals");
        assert_parity("swing_trader hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("swing_trader hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
