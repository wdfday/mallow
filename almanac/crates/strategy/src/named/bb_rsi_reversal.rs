use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{BBands, Rsi};

/// Bollinger Band lower-touch + RSI oversold double confirmation.
///
/// Long when price closes below the lower band AND RSI < oversold threshold.
/// Close when price recovers above the middle band OR RSI > overbought threshold.
pub struct BbRsiReversal {
    bb: BBands,
    rsi: Rsi,
    in_position: bool,
    bb_period: usize,
    bb_std: f64,
    rsi_period: usize,
    oversold: f64,
    overbought: f64,
}

impl BbRsiReversal {
    pub fn new(
        bb_period: usize,
        bb_std: f64,
        rsi_period: usize,
        oversold: f64,
        overbought: f64,
    ) -> Self {
        Self {
            bb: BBands::new(bb_period, bb_std),
            rsi: Rsi::new(rsi_period),
            in_position: false,
            bb_period,
            bb_std,
            rsi_period,
            oversold,
            overbought,
        }
    }
}

impl Strategy for BbRsiReversal {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let bb = self.bb.update(bar.close);
        let rsi = self.rsi.update(bar.close);

        let (Some(bb), Some(rsi)) = (bb, rsi) else {
            return vec![];
        };

        if bar.close < bb.lower && rsi < self.oversold && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (bar.close > bb.middle || rsi > self.overbought) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "bb_rsi_reversal"
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.rsi = Rsi::new(self.rsi_period);
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
    fn bb_rsi_reversal_parity() {
        let bars = bb_rsi_bars();

        let mut hc = BbRsiReversal::new(20, 2.0, 14, 35.0, 65.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "bb":  { "type": "bbands", "period": 20, "multiplier": 2.0 },
                "rsi": { "type": "rsi",    "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "lt",
                  "compare": "bb", "compare_field": "lower" },
                { "source": "rsi", "field": "value", "op": "lt", "value": 35.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "close", "field": "value", "op": "gt",
                  "compare": "bb", "compare_field": "middle" },
                { "source": "rsi", "field": "value", "op": "gt", "value": 65.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close < bb_lower(20) && rsi(14) < 35.0",
            "exit":  "close > bb_mid(20) || rsi(14) > 65.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "bb_rsi_reversal: no signals");
        assert_parity("bb_rsi_reversal hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("bb_rsi_reversal hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
