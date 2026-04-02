use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, HeikenAshi};

/// Bot — Heiken Ashi Color.
///
/// Long when HA candle flips to bullish (red → green).
/// Close when HA candle flips to bearish.
pub struct HaColor {
    ha: HeikenAshi,
    smooth: usize,
    prev_bullish: Option<bool>,
    in_position: bool,
}

impl HaColor {
    /// `smooth = 1` = standard HA, no EMA smoothing.
    pub fn new(smooth: usize) -> Self {
        Self {
            ha: HeikenAshi::new(smooth),
            smooth,
            prev_bullish: None,
            in_position: false,
        }
    }
}

impl Strategy for HaColor {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.ha.update(bar.open, bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let prev = self.prev_bullish.replace(v.is_bullish);
        let Some(was_bullish) = prev else {
            return vec![];
        };

        if v.is_bullish && !was_bullish && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.is_bullish && was_bullish && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "heiken_ashi_color"
    }

    fn reset(&mut self) {
        self.ha = HeikenAshi::new(self.smooth);
        self.prev_bullish = None;
        self.in_position = false;
    }
}

/// Bot — Heiken Ashi Breakout.
///
/// Long after `consecutive_bars` consecutive bullish HA candles.
/// Close after `consecutive_bars` consecutive bearish HA candles.
pub struct HaBreakout {
    ha: HeikenAshi,
    smooth: usize,
    consecutive_bars: usize,
    bull_count: usize,
    bear_count: usize,
    in_position: bool,
}

impl HaBreakout {
    pub fn new(smooth: usize, consecutive_bars: usize) -> Self {
        Self {
            ha: HeikenAshi::new(smooth),
            smooth,
            consecutive_bars,
            bull_count: 0,
            bear_count: 0,
            in_position: false,
        }
    }
}

impl Strategy for HaBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.ha.update(bar.open, bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if v.is_bullish {
            self.bull_count += 1;
            self.bear_count = 0;
        } else {
            self.bear_count += 1;
            self.bull_count = 0;
        }

        if self.bull_count >= self.consecutive_bars && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.bear_count >= self.consecutive_bars && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "heiken_ashi_breakout"
    }

    fn reset(&mut self) {
        self.ha = HeikenAshi::new(self.smooth);
        self.bull_count = 0;
        self.bear_count = 0;
        self.in_position = false;
    }
}

/// Bot — Heiken Ashi Harmonizer.
///
/// Long when HA is bullish AND close > EMA(period).
/// Close when HA turns bearish OR close < EMA.
pub struct HaHarmonizer {
    ha: HeikenAshi,
    ema: Ema,
    smooth: usize,
    ema_period: usize,
    in_position: bool,
}

impl HaHarmonizer {
    pub fn new(smooth: usize, ema_period: usize) -> Self {
        Self {
            ha: HeikenAshi::new(smooth),
            ema: Ema::new(ema_period),
            smooth,
            ema_period,
            in_position: false,
        }
    }
}

impl Strategy for HaHarmonizer {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let ha = self.ha.update(bar.open, bar.high, bar.low, bar.close);
        let Some(ema) = self.ema.update(bar.close) else {
            return vec![];
        };
        let Some(ha) = ha else {
            return vec![];
        };

        let bullish_setup = ha.is_bullish && bar.close > ema;

        if bullish_setup && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if (!ha.is_bullish || bar.close < ema) && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "heiken_ashi_harmonizer"
    }

    fn reset(&mut self) {
        self.ha = HeikenAshi::new(self.smooth);
        self.ema = Ema::new(self.ema_period);
        self.in_position = false;
    }
}
