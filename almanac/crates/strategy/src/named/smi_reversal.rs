use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Smi;

/// Stochastic Momentum Index oversold/overbought reversal.
///
/// SMI improves on classic Stochastic by measuring distance from the midpoint
/// of the high-low range rather than from the low.
///
/// Long when SMI crosses above its signal line from the oversold zone (< -40).
/// Close when SMI reaches overbought (> 40) OR crosses below signal.
pub struct SmiReversal {
    smi: Smi,
    prev_smi: Option<f64>,
    prev_signal: Option<f64>,
    in_position: bool,
    period: usize,
    smooth1: usize,
    smooth2: usize,
    signal_period: usize,
    oversold: f64,
    overbought: f64,
}

impl SmiReversal {
    pub fn new(
        period: usize,
        smooth1: usize,
        smooth2: usize,
        signal_period: usize,
        oversold: f64,
        overbought: f64,
    ) -> Self {
        Self {
            smi: Smi::new(period, smooth1, smooth2, signal_period),
            prev_smi: None,
            prev_signal: None,
            in_position: false,
            period,
            smooth1,
            smooth2,
            signal_period,
            oversold,
            overbought,
        }
    }
}

impl Strategy for SmiReversal {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(sv) = self.smi.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let (Some(ps), Some(psig)) = (self.prev_smi, self.prev_signal) else {
            self.prev_smi = Some(sv.smi);
            self.prev_signal = Some(sv.signal);
            return vec![];
        };

        let crossed_above_signal = ps <= psig && sv.smi > sv.signal;
        let crossed_below_signal = ps >= psig && sv.smi < sv.signal;

        self.prev_smi = Some(sv.smi);
        self.prev_signal = Some(sv.signal);

        if crossed_above_signal && ps < self.oversold && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (sv.smi > self.overbought || crossed_below_signal) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "smi_reversal"
    }

    fn reset(&mut self) {
        self.smi = Smi::new(self.period, self.smooth1, self.smooth2, self.signal_period);
        self.prev_smi = None;
        self.prev_signal = None;
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
    fn smi_reversal_parity() {
        let bars = rsi_bars(300);

        let mut hc = SmiReversal::new(13, 25, 2, 9, -40.0, 40.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "smi": { "type": "smi" } },
            "entry": { "logic": "and", "rules": [
                { "source": "smi", "field": "smi",    "op": "cross_above",
                  "compare": "smi", "compare_field": "signal" },
                { "source": "smi", "field": "smi",    "op": "lt", "value": -40.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "smi", "field": "smi", "op": "gt", "value": 40.0 },
                { "source": "smi", "field": "smi", "op": "cross_below",
                  "compare": "smi", "compare_field": "signal" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_smi_line() <= prev_smi_sig() && smi_line() > smi_sig() && prev_smi_line() < -40.0",
            "exit":  "smi_line() > 40.0 || (prev_smi_line() >= prev_smi_sig() && smi_line() < smi_sig())"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "smi_reversal: no signals");
        assert_parity("smi_reversal hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("smi_reversal hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
