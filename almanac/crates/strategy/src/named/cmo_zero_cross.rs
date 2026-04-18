use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Cmo, Ema};

/// Chande Momentum Oscillator zero-line cross + EMA trend filter.
///
/// Long when CMO crosses above zero (positive momentum) and price > EMA.
/// Close when CMO crosses below zero or price falls below EMA.
pub struct CmoZeroCross {
    cmo: Cmo,
    ema: Ema,
    prev_cmo: Option<f64>,
    in_position: bool,
    cmo_period: usize,
    ema_period: usize,
}

impl CmoZeroCross {
    pub fn new(cmo_period: usize, ema_period: usize) -> Self {
        Self {
            cmo: Cmo::new(cmo_period),
            ema: Ema::new(ema_period),
            prev_cmo: None,
            in_position: false,
            cmo_period,
            ema_period,
        }
    }
}

impl Strategy for CmoZeroCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let cmo = self.cmo.update(bar.close);
        let ema = self.ema.update(bar.close);

        let (Some(cmo), Some(ema)) = (cmo, ema) else {
            return vec![];
        };

        let Some(prev) = self.prev_cmo else {
            self.prev_cmo = Some(cmo);
            return vec![];
        };

        let crossed_above_zero = prev <= 0.0 && cmo > 0.0;
        let crossed_below_zero = prev >= 0.0 && cmo < 0.0;
        self.prev_cmo = Some(cmo);

        if crossed_above_zero && bar.close > ema && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (crossed_below_zero || bar.close < ema) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "cmo_zero_cross"
    }

    fn reset(&mut self) {
        self.cmo = Cmo::new(self.cmo_period);
        self.ema = Ema::new(self.ema_period);
        self.prev_cmo = None;
        self.in_position = false;
    }
}
