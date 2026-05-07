use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Rsi, Vwma};

/// Volume-Weighted MA + RSI momentum filter.
///
/// VWMA above SMA signals institutional accumulation. RSI > 50 confirms momentum.
///
/// Long when price > VWMA AND RSI > rsi_entry.
/// Close when price < VWMA OR RSI < rsi_exit.
pub struct VwmaRsi {
    vwma: Vwma,
    rsi: Rsi,
    in_position: bool,
    vwma_period: usize,
    rsi_period: usize,
    rsi_entry: f64,
    rsi_exit: f64,
}

impl VwmaRsi {
    pub fn new(vwma_period: usize, rsi_period: usize, rsi_entry: f64, rsi_exit: f64) -> Self {
        Self {
            vwma: Vwma::new(vwma_period),
            rsi: Rsi::new(rsi_period),
            in_position: false,
            vwma_period,
            rsi_period,
            rsi_entry,
            rsi_exit,
        }
    }
}

impl Strategy for VwmaRsi {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let vwma = self.vwma.update(bar.close, bar.volume);
        let rsi = self.rsi.update(bar.close);

        let (Some(vwma), Some(rsi)) = (vwma, rsi) else {
            return vec![];
        };

        if bar.close > vwma && rsi > self.rsi_entry && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (bar.close < vwma || rsi < self.rsi_exit) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "vwma_rsi"
    }

    fn reset(&mut self) {
        self.vwma = Vwma::new(self.vwma_period);
        self.rsi = Rsi::new(self.rsi_period);
        self.in_position = false;
    }
}
