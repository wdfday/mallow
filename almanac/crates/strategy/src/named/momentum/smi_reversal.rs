use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Smi;

const RHAI: &str = r#"
let sm = ind.smi(13);
if sm[1].smi <= sm[1].signal && sm[0].smi > sm[0].signal && sm[1].smi < -40.0 { entry = true; }
if sm[0].smi > 40.0 || (sm[1].smi >= sm[1].signal && sm[0].smi < sm[0].signal) { exit  = true; }
"#;

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

        if crossed_above_signal && ps < self.oversold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if sv.smi > self.overbought || crossed_below_signal {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "smi_reversal"
    }

    fn description(&self) -> &'static str {
        "Long when SMI crosses above its signal from oversold zone (< -40). Exit when overbought or SMI crosses back below signal."
    }

    fn script(&self) -> Option<&'static str> { Some(RHAI) }

    fn reset(&mut self) {
        self.smi = Smi::new(self.period, self.smooth1, self.smooth2, self.signal_period);
        self.prev_smi = None;
        self.prev_signal = None;
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
    fn script_parity() {
        let bars = rsi_bars(300);

        let mut named = SmiReversal::new(13, 25, 2, 9, -40.0, 40.0);
        let named_sigs = run(&mut named, &bars);

        // SMI Multi: .smi .signal; entry requires previous SMI was in oversold zone
        let script = SmiReversal::new(13, 25, 2, 9, -40.0, 40.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "smi_reversal: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}