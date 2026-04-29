use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Uo;

/// Ultimate Oscillator (Larry Williams) oversold reversal.
///
/// Long when UO crosses above the oversold level.
/// Close when UO reaches overbought level.
pub struct UoReversal {
    uo: Uo,
    prev_uo: Option<f64>,
    in_position: bool,
    fast: usize,
    medium: usize,
    slow: usize,
    oversold: f64,
    overbought: f64,
}

impl UoReversal {
    pub fn new(fast: usize, medium: usize, slow: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            uo: Uo::new(fast, medium, slow),
            prev_uo: None,
            in_position: false,
            fast,
            medium,
            slow,
            oversold,
            overbought,
        }
    }
}

impl Strategy for UoReversal {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(uo) = self.uo.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let Some(prev) = self.prev_uo else {
            self.prev_uo = Some(uo);
            return vec![];
        };

        let crossed_oversold_up = prev <= self.oversold && uo > self.oversold;
        self.prev_uo = Some(uo);

        if crossed_oversold_up && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && uo > self.overbought {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "uo_reversal"
    }

    fn reset(&mut self) {
        self.uo = Uo::new(self.fast, self.medium, self.slow);
        self.prev_uo = None;
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
    fn uo_reversal_parity() {
        let bars = rsi_bars(200);

        let mut hc = UoReversal::new(7, 14, 28, 30.0, 70.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "uo": { "type": "uo", "fast": 7, "medium": 14, "slow": 28 } },
            "entry": { "logic": "and", "rules": [
                { "source": "uo", "field": "value", "op": "cross_above", "value": 30.0 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "uo", "field": "value", "op": "gt", "value": 70.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_uo() <= 30.0 && uo() > 30.0",
            "exit":  "uo() > 70.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "uo_reversal: no signals");
        assert_parity("uo_reversal hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("uo_reversal hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
