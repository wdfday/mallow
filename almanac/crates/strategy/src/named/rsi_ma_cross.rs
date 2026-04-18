use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, Rsi};

/// EMA crossover confirmed by RSI momentum filter.
///
/// Long when fast EMA crosses above slow EMA AND RSI > 50 (bullish momentum).
/// Close when fast EMA crosses below slow EMA OR RSI < 45 (momentum fading).
pub struct RsiMaCross {
    fast: Ema,
    slow: Ema,
    rsi: Rsi,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    in_position: bool,
    fast_period: usize,
    slow_period: usize,
    rsi_period: usize,
    rsi_entry: f64,
    rsi_exit: f64,
}

impl RsiMaCross {
    pub fn new(
        fast_period: usize,
        slow_period: usize,
        rsi_period: usize,
        rsi_entry: f64,
        rsi_exit: f64,
    ) -> Self {
        Self {
            fast: Ema::new(fast_period),
            slow: Ema::new(slow_period),
            rsi: Rsi::new(rsi_period),
            prev_fast: None,
            prev_slow: None,
            in_position: false,
            fast_period,
            slow_period,
            rsi_period,
            rsi_entry,
            rsi_exit,
        }
    }
}

impl Strategy for RsiMaCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);
        let rsi = self.rsi.update(bar.close);

        let (Some(f), Some(s), Some(r)) = (fast, slow, rsi) else {
            return vec![];
        };

        let (Some(pf), Some(ps)) = (self.prev_fast, self.prev_slow) else {
            self.prev_fast = Some(f);
            self.prev_slow = Some(s);
            return vec![];
        };

        let crossed_above = pf <= ps && f > s;
        let crossed_below = pf >= ps && f < s;
        self.prev_fast = Some(f);
        self.prev_slow = Some(s);

        if crossed_above && r > self.rsi_entry && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (crossed_below || r < self.rsi_exit) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "rsi_ma_cross"
    }

    fn reset(&mut self) {
        self.fast = Ema::new(self.fast_period);
        self.slow = Ema::new(self.slow_period);
        self.rsi = Rsi::new(self.rsi_period);
        self.prev_fast = None;
        self.prev_slow = None;
        self.in_position = false;
    }
}
