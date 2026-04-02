//! Bot 40 — 3 PIXEL strategy.
//!
//! Original: AmiBroker AFL "3 PIXEL" by DNSE/AmiX platform.
//!
//! Logic (decoded from AFL source):
//!   TS5 = Close > midpoint(Highest High / Lowest Low over 5 bars)
//!   TS4 = Close > midpoint(Highest High / Lowest Low over 20 bars)
//!   TS3 = Close > midpoint(Highest High / Lowest Low over 60 bars)
//!
//!   Buy  when ≥2 of 3 pixels are green (transition from not all green)
//!   Sell when all 3 pixels are red (i.e. Close below all midpoints)
//!
//! In essence this is a multi-timeframe midpoint breakout strategy.
//! The three "pixels" visualise short / medium / long-term trend alignment.

use std::collections::VecDeque;

use alm_core::{Bar, signal::Signal};
use alm_core::strategy::Strategy;
use alm_core::portfolio::PortfolioSnapshot;

pub struct Pixel3 {
    symbol: String,
    /// Rolling windows for high/low tracking.
    window_short:  VecDeque<(f64, f64)>, // (high, low) per bar — 5 bars
    window_medium: VecDeque<(f64, f64)>, // 20 bars
    window_long:   VecDeque<(f64, f64)>, // 60 bars
    period_short:  usize,
    period_medium: usize,
    period_long:   usize,
    /// Whether we currently hold a position.
    in_position: bool,
}

impl Pixel3 {
    pub fn new(symbol: impl Into<String>) -> Self {
        Self::with_periods(symbol, 5, 20, 60)
    }

    pub fn with_periods(
        symbol: impl Into<String>,
        period_short: usize,
        period_medium: usize,
        period_long: usize,
    ) -> Self {
        Self {
            symbol: symbol.into(),
            window_short: VecDeque::new(),
            window_medium: VecDeque::new(),
            window_long: VecDeque::new(),
            period_short,
            period_medium,
            period_long,
            in_position: false,
        }
    }

    /// Compute midpoint of highest-high and lowest-low in a window.
    fn midpoint(window: &VecDeque<(f64, f64)>) -> f64 {
        let highest = window.iter().map(|(h, _)| *h).fold(f64::NEG_INFINITY, f64::max);
        let lowest  = window.iter().map(|(_, l)| *l).fold(f64::INFINITY,     f64::min);
        (highest + lowest) / 2.0
    }

    fn push_bar(window: &mut VecDeque<(f64, f64)>, bar: &Bar, max_len: usize) {
        if window.len() >= max_len {
            window.pop_front();
        }
        window.push_back((bar.high, bar.low));
    }
}

impl Strategy for Pixel3 {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        // Lazily capture symbol from the first bar (supports factory construction with "").
        if self.symbol.is_empty() {
            self.symbol = bar.symbol.clone();
        }

        Self::push_bar(&mut self.window_short,  bar, self.period_short);
        Self::push_bar(&mut self.window_medium, bar, self.period_medium);
        Self::push_bar(&mut self.window_long,   bar, self.period_long);

        // Need full windows before signalling.
        if self.window_long.len() < self.period_long {
            return vec![];
        }

        let ts5 = bar.close > Self::midpoint(&self.window_short);
        let ts4 = bar.close > Self::midpoint(&self.window_medium);
        let ts3 = bar.close > Self::midpoint(&self.window_long);

        let green_count = [ts5, ts4, ts3].iter().filter(|&&v| v).count();
        let all_red = green_count == 0;

        if !self.in_position && green_count >= 2 {
            self.in_position = true;
            let strength = green_count as f64 / 3.0; // 0.67 or 1.0
            return vec![Signal::long(bar.timestamp, &self.symbol, strength)];
        }

        if self.in_position && all_red {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &self.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "pixel_3"
    }

    fn reset(&mut self) {
        self.window_short.clear();
        self.window_medium.clear();
        self.window_long.clear();
        self.in_position = false;
    }

    fn set_portfolio_snapshot(&mut self, snapshot: &PortfolioSnapshot) {
        self.in_position = snapshot.is_long(&self.symbol);
    }
}
