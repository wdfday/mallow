use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Macd, Sma};

/// Bot #3 — MACD with MA filter.
///
/// Long when MACD histogram crosses above zero AND price is above MA.
/// Closes when histogram crosses below zero OR price drops below MA.
pub struct MacdMa {
    macd: Macd,
    ma: Sma,
    prev_hist: Option<f64>,
    in_position: bool,
    fast: usize,
    slow: usize,
    signal_period: usize,
    ma_period: usize,
}

impl MacdMa {
    pub fn new(fast: usize, slow: usize, signal_period: usize, ma_period: usize) -> Self {
        Self {
            macd: Macd::new(fast, slow, signal_period),
            ma: Sma::new(ma_period),
            prev_hist: None,
            in_position: false,
            fast,
            slow,
            signal_period,
            ma_period,
        }
    }
}

impl Strategy for MacdMa {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let macd_val = self.macd.update(bar.close);
        let ma_val = self.ma.update(bar.close);

        let (Some(m), Some(ma)) = (macd_val, ma_val) else {
            return vec![];
        };

        let Some(prev) = self.prev_hist else {
            self.prev_hist = Some(m.histogram);
            return vec![];
        };

        let hist_crossed_up = prev <= 0.0 && m.histogram > 0.0;
        let hist_crossed_down = prev >= 0.0 && m.histogram < 0.0;
        self.prev_hist = Some(m.histogram);

        if hist_crossed_up && bar.close > ma && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (hist_crossed_down || bar.close < ma) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "macd_ma"
    }

    fn reset(&mut self) {
        self.macd = Macd::new(self.fast, self.slow, self.signal_period);
        self.ma = Sma::new(self.ma_period);
        self.prev_hist = None;
        self.in_position = false;
    }
}
