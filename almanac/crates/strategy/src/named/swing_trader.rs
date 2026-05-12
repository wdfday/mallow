use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Adx, Cci};

/// Bot #32 — Swing Trader.
///
/// Long when CCI breaks above +100 (strong upside momentum) AND ADX > threshold
/// (confirms the move is part of a trending environment, not just noise).
/// Closes when CCI crosses back below −100.
pub struct SwingTrader {
    cci: Cci,
    adx: Adx,
    prev_cci: Option<f64>,
    adx_threshold: f64,
    cci_period: usize,
    adx_period: usize,
}

impl SwingTrader {
    pub fn new(cci_period: usize, adx_period: usize, adx_threshold: f64) -> Self {
        Self {
            cci: Cci::new(cci_period),
            adx: Adx::new(adx_period),
            prev_cci: None,
            adx_threshold,
            cci_period,
            adx_period,
        }
    }
}

impl Strategy for SwingTrader {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let cci_val = self.cci.update(bar.high, bar.low, bar.close);
        let adx_val = self.adx.update(bar.high, bar.low, bar.close);

        let (Some(cci), Some(adx)) = (cci_val, adx_val) else {
            return vec![];
        };

        let Some(prev) = self.prev_cci else {
            self.prev_cci = Some(cci);
            return vec![];
        };

        let cci_crossed_above_100 = prev <= 100.0 && cci > 100.0;
        let cci_crossed_below_minus100 = prev >= -100.0 && cci < -100.0;

        self.prev_cci = Some(cci);

        if cci_crossed_above_100 && adx.adx > self.adx_threshold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if cci_crossed_below_minus100 {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "swing_trader"
    }

    fn reset(&mut self) {
        self.cci = Cci::new(self.cci_period);
        self.adx = Adx::new(self.adx_period);
        self.prev_cci = None;
    }
}
