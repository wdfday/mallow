use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Adx;

/// Bot #14 — DMI and ADX.
///
/// Long when ADX > threshold AND +DI crosses above -DI.
/// Closes when -DI crosses above +DI.
pub struct DmiAdx {
    adx: Adx,
    prev_plus_di: Option<f64>,
    prev_minus_di: Option<f64>,
    adx_threshold: f64,
    in_position: bool,
    period: usize,
}

impl DmiAdx {
    pub fn new(period: usize, adx_threshold: f64) -> Self {
        Self {
            adx: Adx::new(period),
            prev_plus_di: None,
            prev_minus_di: None,
            adx_threshold,
            in_position: false,
            period,
        }
    }
}

impl Strategy for DmiAdx {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.adx.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let (Some(pp), Some(pm)) = (self.prev_plus_di, self.prev_minus_di) else {
            self.prev_plus_di = Some(v.plus_di);
            self.prev_minus_di = Some(v.minus_di);
            return vec![];
        };

        let plus_crossed_above = pp <= pm && v.plus_di > v.minus_di;
        let minus_crossed_above = pm <= pp && v.minus_di > v.plus_di;

        self.prev_plus_di = Some(v.plus_di);
        self.prev_minus_di = Some(v.minus_di);

        if plus_crossed_above && v.adx > self.adx_threshold && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, v.adx / 100.0)];
        }
        if minus_crossed_above && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "dmi_adx"
    }

    fn reset(&mut self) {
        self.adx = Adx::new(self.period);
        self.prev_plus_di = None;
        self.prev_minus_di = None;
        self.in_position = false;
    }
}
