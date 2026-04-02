use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Gmma;

/// Bot — GMMA Crossover (Guppy Multiple Moving Average).
///
/// Long when all 6 short-term EMAs are above all 6 long-term EMAs (bullish alignment).
/// Close when any short-term EMA drops below any long-term EMA (alignment breaks).
///
/// Standard periods: short = [3,5,8,10,12,15], long = [30,35,40,45,50,60].
pub struct GmmaCrossover {
    gmma: Gmma,
    prev_bullish: Option<bool>,
    in_position: bool,
}

impl GmmaCrossover {
    pub fn new() -> Self {
        Self {
            gmma: Gmma::default(),
            prev_bullish: None,
            in_position: false,
        }
    }
}

impl Default for GmmaCrossover {
    fn default() -> Self {
        Self::new()
    }
}

impl Strategy for GmmaCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.gmma.update(bar.close) else {
            return vec![];
        };

        let prev = self.prev_bullish.replace(v.bullish);
        let Some(was_bullish) = prev else {
            return vec![];
        };

        if v.bullish && !was_bullish && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.bullish && was_bullish && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "gmma_crossover"
    }

    fn reset(&mut self) {
        self.gmma = Gmma::default();
        self.prev_bullish = None;
        self.in_position = false;
    }
}
