use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Adx, Ema};

/// Bot #34 — Trend Transition Tracker.
///
/// Long when fast EMA crosses above slow EMA AND ADX > threshold (trend is strong).
/// Closes when fast EMA crosses back below slow EMA.
pub struct TrendTransition {
    fast_ema: Ema,
    slow_ema: Ema,
    adx: Adx,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    adx_threshold: f64,
    in_position: bool,
    fast_period: usize,
    slow_period: usize,
    adx_period: usize,
}

impl TrendTransition {
    pub fn new(fast_period: usize, slow_period: usize, adx_period: usize, adx_threshold: f64) -> Self {
        Self {
            fast_ema: Ema::new(fast_period),
            slow_ema: Ema::new(slow_period),
            adx: Adx::new(adx_period),
            prev_fast: None,
            prev_slow: None,
            adx_threshold,
            in_position: false,
            fast_period,
            slow_period,
            adx_period,
        }
    }
}

impl Strategy for TrendTransition {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast_val = self.fast_ema.update(bar.close);
        let slow_val = self.slow_ema.update(bar.close);
        let adx_val = self.adx.update(bar.high, bar.low, bar.close);

        let (Some(f), Some(s), Some(adx)) = (fast_val, slow_val, adx_val) else {
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

        if crossed_above && adx.adx > self.adx_threshold && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "trend_transition"
    }

    fn reset(&mut self) {
        self.fast_ema = Ema::new(self.fast_period);
        self.slow_ema = Ema::new(self.slow_period);
        self.adx = Adx::new(self.adx_period);
        self.prev_fast = None;
        self.prev_slow = None;
        self.in_position = false;
    }
}
