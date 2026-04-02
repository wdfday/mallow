use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Atr;
use std::collections::VecDeque;

/// Chandelier Exit — ATR-based trailing stop system.
///
/// Long  when close breaks above the N-bar highest high.
/// Exit  when close drops below (highest_high - multiplier * ATR) — the chandelier stop.
///
/// Default: period=22, multiplier=3.0
pub struct ChandelierExit {
    atr: Atr,
    period: usize,
    multiplier: f64,
    atr_p: usize,
    highs: VecDeque<f64>,
    highest_high: f64,
    in_position: bool,
    chandelier_stop: f64,
}

impl ChandelierExit {
    pub fn new(period: usize, multiplier: f64) -> Self {
        Self {
            atr: Atr::new(period),
            period,
            multiplier,
            atr_p: period,
            highs: VecDeque::with_capacity(period),
            highest_high: f64::NEG_INFINITY,
            in_position: false,
            chandelier_stop: 0.0,
        }
    }
}

impl Strategy for ChandelierExit {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        // Update rolling highs
        self.highs.push_back(bar.high);
        if self.highs.len() > self.period { self.highs.pop_front(); }

        let Some(atr_val) = self.atr.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };
        if self.highs.len() < self.period { return vec![]; }

        let hh = self.highs.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        self.chandelier_stop = hh - self.multiplier * atr_val.atr;

        // Entry: close above previous highest high (breakout)
        if bar.close > self.highest_high && self.highest_high != f64::NEG_INFINITY && !self.in_position {
            self.in_position = true;
            self.highest_high = hh;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }

        // Update trailing highest high while in position
        if self.in_position && hh > self.highest_high {
            self.highest_high = hh;
            self.chandelier_stop = hh - self.multiplier * atr_val.atr;
        }

        // Exit: close drops below chandelier stop
        if self.in_position && bar.close < self.chandelier_stop {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        // Track highest high even when not in position
        if !self.in_position { self.highest_high = hh; }

        vec![]
    }

    fn name(&self) -> &str {
        "chandelier_exit"
    }

    fn reset(&mut self) {
        self.atr = Atr::new(self.atr_p);
        self.highs.clear();
        self.highest_high = f64::NEG_INFINITY;
        self.in_position = false;
        self.chandelier_stop = 0.0;
    }
}
