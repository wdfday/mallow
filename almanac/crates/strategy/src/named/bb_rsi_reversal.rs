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
