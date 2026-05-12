use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Adx;

/// Bot #46 — Wolfstein Trending.
///
/// Enters long when ADX > `long_threshold` AND +DI > -DI (strong bullish trend).
/// Exits when ADX falls below `short_threshold` (trend weakening).
pub struct Wolfstein {
    adx: Adx,
    long_threshold: f64,
    short_threshold: f64,
    period: usize,
}

impl Wolfstein {
    pub fn new(period: usize, long_threshold: f64, short_threshold: f64) -> Self {
        Self {
            adx: Adx::new(period),
            long_threshold,
            short_threshold,
            period,
        }
    }
}

impl Strategy for Wolfstein {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.adx.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if v.adx > self.long_threshold && v.plus_di > v.minus_di {
            return vec![Signal::long(bar.timestamp, &bar.symbol, v.adx / 100.0)];
        }
        if v.adx < self.short_threshold {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "wolfstein"
    }

    fn reset(&mut self) {
        self.adx = Adx::new(self.period);
    }
}
