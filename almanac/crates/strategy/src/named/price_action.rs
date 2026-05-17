use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Atr, Ema};
use std::collections::VecDeque;

/// Price Action Swing — support/resistance breakout with pin bar filter.
///
/// Detects:
/// 1. Pivot high/low over `swing_bars` bars on each side
/// 2. Breakout above swing high → Long (momentum confirmation)
/// 3. Stops at recent swing low (structure-based exit)
///
/// Optional: bullish pin bar (long lower wick) at support → early long entry.
///
/// Default: swing_bars=5, ema_trend=50, atr_period=14
pub struct PriceActionSwing {
    ema: Ema,
    atr: Atr,
    swing_bars: usize,
    ema_p: usize,
    atr_p: usize,
    window: VecDeque<Bar>,
    swing_high: f64,
    swing_low: f64,
    in_position: bool,
    stop_level: f64,
}

impl PriceActionSwing {
    pub fn new(swing_bars: usize, ema_trend: usize, atr_period: usize) -> Self {
        let cap = swing_bars * 2 + 1;
        Self {
            ema: Ema::new(ema_trend),
            atr: Atr::new(atr_period),
            swing_bars,
            ema_p: ema_trend,
            atr_p: atr_period,
            window: VecDeque::with_capacity(cap),
            swing_high: f64::NEG_INFINITY,
            swing_low: f64::INFINITY,
            in_position: false,
            stop_level: 0.0,
        }
    }

    /// Check if the center bar of the window is a pivot high
    fn is_pivot_high(&self) -> Option<f64> {
        let n = self.swing_bars;
        if self.window.len() < 2 * n + 1 { return None; }
        let mid = self.window[n].high;
        for i in 0..n {
            if self.window[i].high >= mid || self.window[2 * n - i].high >= mid {
                return None;
            }
        }
        Some(mid)
    }

    /// Check if the center bar of the window is a pivot low
    fn is_pivot_low(&self) -> Option<f64> {
        let n = self.swing_bars;
        if self.window.len() < 2 * n + 1 { return None; }
        let mid = self.window[n].low;
        for i in 0..n {
            if self.window[i].low <= mid || self.window[2 * n - i].low <= mid {
                return None;
            }
        }
        Some(mid)
    }

    /// Bullish pin bar: lower wick > 2x body, upper wick small
    fn is_bullish_pin(bar: &Bar) -> bool {
        let body = (bar.close - bar.open).abs();
        let lower_wick = bar.open.min(bar.close) - bar.low;
        let upper_wick = bar.high - bar.open.max(bar.close);
        body > 0.0 && lower_wick > 2.0 * body && upper_wick < body
    }
}

impl Strategy for PriceActionSwing {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        self.window.push_back(bar.clone());
        let cap = self.swing_bars * 2 + 1;
        if self.window.len() > cap { self.window.pop_front(); }

        let ema = self.ema.update(bar.close);
        let atr_val = self.atr.update(bar.high, bar.low, bar.close);

        let (Some(ema), Some(atr_val)) = (ema, atr_val) else {
            return vec![];
        };

        // Update swing levels
        if let Some(ph) = self.is_pivot_high() { self.swing_high = ph; }
        if let Some(pl) = self.is_pivot_low() { self.swing_low = pl; }

        if self.swing_high == f64::NEG_INFINITY || self.swing_low == f64::INFINITY {
            return vec![];
        }

        let above_trend = bar.close > ema;

        if !self.in_position {
            // Entry 1: Breakout above swing high in uptrend
            if bar.close > self.swing_high && above_trend {
                self.in_position = true;
                self.stop_level = self.swing_low;
                return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
            }

            // Entry 2: Bullish pin bar at or near swing low (support bounce)
            let near_support = (bar.close - self.swing_low).abs() < atr_val.atr * 0.5;
            if Self::is_bullish_pin(bar) && near_support && above_trend {
                self.in_position = true;
                self.stop_level = bar.low - atr_val.atr * 0.5;
                return vec![Signal::long(bar.timestamp, &bar.symbol, 0.8)];
            }
        } else {
            // Exit: close below stop level
            if bar.close < self.stop_level {
                self.in_position = false;
                return vec![Signal::exit(bar.timestamp, &bar.symbol)];
            }
            // Trail stop to latest swing low
            if self.swing_low > self.stop_level {
                self.stop_level = self.swing_low;
            }
        }

        vec![]
    }

    fn name(&self) -> &str {
        "price_action_swing"
    }

    fn reset(&mut self) {
        self.ema = Ema::new(self.ema_p);
        self.atr = Atr::new(self.atr_p);
        self.window.clear();
        self.swing_high = f64::NEG_INFINITY;
        self.swing_low = f64::INFINITY;
        self.in_position = false;
        self.stop_level = 0.0;
    }
}
