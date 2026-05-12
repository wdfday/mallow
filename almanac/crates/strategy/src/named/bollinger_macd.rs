use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{BBands, Macd};

/// Bot #12 — Bollinger Breakthrough + MACD histogram.
///
/// Long when price breaks above upper Bollinger Band AND MACD histogram > 0.
/// Closes when price falls below middle band OR histogram turns negative.
pub struct BollingerMacd {
    bb: BBands,
    macd: Macd,
    bb_period: usize,
    bb_std: f64,
    fast: usize,
    slow: usize,
    signal_period: usize,
}

impl BollingerMacd {
    pub fn new(bb_period: usize, bb_std: f64, fast: usize, slow: usize, signal_period: usize) -> Self {
        Self {
            bb: BBands::new(bb_period, bb_std),
            macd: Macd::new(fast, slow, signal_period),
            bb_period,
            bb_std,
            fast,
            slow,
            signal_period,
        }
    }
}

impl Strategy for BollingerMacd {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let bb_val = self.bb.update(bar.close);
        let macd_val = self.macd.update(bar.close);

        let (Some(bb), Some(m)) = (bb_val, macd_val) else {
            return vec![];
        };

        if bar.close > bb.upper && m.histogram > 0.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < bb.middle || m.histogram < 0.0 {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "bollinger_macd"
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.macd = Macd::new(self.fast, self.slow, self.signal_period);
    }
}

#[cfg(test)]
mod tests {

}
