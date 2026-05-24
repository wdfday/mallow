use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Atr;
use std::collections::VecDeque;

/// Chandelier Exit — ATR-based trailing stop system.
///
/// Bullish when close > (rolling-highest-high - multiplier * ATR).
/// Long on transition from non-bullish to bullish.
/// Exit on transition from bullish to non-bullish.
///
/// Default: period=22, multiplier=3.0
pub struct ChandelierExit {
    atr: Atr,
    period: usize,
    multiplier: f64,
    atr_p: usize,
    highs: VecDeque<f64>,
    in_position: bool,
    prev_bull: Option<bool>,
}

impl ChandelierExit {
    pub fn new(period: usize, multiplier: f64) -> Self {
        Self {
            atr: Atr::new(period),
            period,
            multiplier,
            atr_p: period,
            highs: VecDeque::with_capacity(period),
            in_position: false,
            prev_bull: None,
        }
    }
}

impl Strategy for ChandelierExit {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        self.highs.push_back(bar.high);
        if self.highs.len() > self.period { self.highs.pop_front(); }

        let Some(atr_val) = self.atr.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };
        if self.highs.len() < self.period { return vec![]; }

        let hh = self.highs.iter().cloned().fold(f64::NEG_INFINITY, f64::max);
        let stop = hh - self.multiplier * atr_val.atr;
        let bull = bar.close > stop;

        let was_bull = self.prev_bull.replace(bull);
        let Some(prev) = was_bull else {
            return vec![];
        };

        if !prev && bull && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if prev && !bull && self.in_position {
            self.in_position = false;
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "chandelier_exit"
    }

    fn description(&self) -> &'static str {
        "Long when close crosses above chandelier stop (HH - mult*ATR). Exit when close drops below it."
    }

    fn reset(&mut self) {
        self.atr = Atr::new(self.atr_p);
        self.highs.clear();
        self.in_position = false;
        self.prev_bull = None;
    }
}
