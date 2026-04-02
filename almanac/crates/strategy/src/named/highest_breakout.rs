use std::collections::VecDeque;

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};

/// Bot — Highest High Breakout (Donchian-style).
///
/// Long when current close breaks above the highest close of the previous `period` bars.
/// Close when close drops below the lowest close of the previous `period` bars.
pub struct HighestBreakout {
    period: usize,
    closes: VecDeque<f64>,
    in_position: bool,
}

impl HighestBreakout {
    pub fn new(period: usize) -> Self {
        assert!(period >= 2);
        Self {
            period,
            closes: VecDeque::with_capacity(period + 1),
            in_position: false,
        }
    }
}

impl Strategy for HighestBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        self.closes.push_back(bar.close);
        if self.closes.len() > self.period + 1 {
            self.closes.pop_front();
        }
        if self.closes.len() < self.period + 1 {
            return vec![];
        }

        // Lookback window = all bars except the current one
        let lookback = self.closes.range(..self.period);
        let highest = lookback.cloned().fold(f64::NEG_INFINITY, f64::max);
        let lowest = self.closes.range(..self.period).cloned().fold(f64::INFINITY, f64::min);

        if bar.close > highest && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < lowest && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "highest_breakout"
    }

    fn reset(&mut self) {
        self.closes.clear();
        self.in_position = false;
    }
}
