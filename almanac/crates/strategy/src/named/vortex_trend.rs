use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Vortex;

/// Vortex Indicator +VI / -VI crossover trend strategy.
///
/// Long when +VI crosses above -VI (upward vortex dominates).
/// Close when -VI crosses above +VI (downward vortex dominates).
pub struct VortexTrend {
    vortex: Vortex,
    prev_plus: Option<f64>,
    prev_minus: Option<f64>,
    in_position: bool,
    period: usize,
}

impl VortexTrend {
    pub fn new(period: usize) -> Self {
        Self {
            vortex: Vortex::new(period),
            prev_plus: None,
            prev_minus: None,
            in_position: false,
            period,
        }
    }
}

impl Strategy for VortexTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(vv) = self.vortex.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let (Some(pp), Some(pm)) = (self.prev_plus, self.prev_minus) else {
            self.prev_plus = Some(vv.plus_vi);
            self.prev_minus = Some(vv.minus_vi);
            return vec![];
        };

        let bull_cross = pp <= pm && vv.plus_vi > vv.minus_vi;
        let bear_cross = pp >= pm && vv.plus_vi < vv.minus_vi;

        self.prev_plus = Some(vv.plus_vi);
        self.prev_minus = Some(vv.minus_vi);

        if bull_cross && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bear_cross && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "vortex_trend"
    }

    fn reset(&mut self) {
        self.vortex = Vortex::new(self.period);
        self.prev_plus = None;
        self.prev_minus = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {

}
