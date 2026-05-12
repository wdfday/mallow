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
    }
}