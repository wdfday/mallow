use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Gmma;

/// Bot — GMMA Crossover (Guppy Multiple Moving Average).
///
/// Uses the average of the short group vs the average of the long group.
/// Long when short_avg crosses above long_avg.
/// Close when short_avg crosses below long_avg.
///
/// Standard periods: short = [3,5,8,10,12,15], long = [30,35,40,45,50,60].
pub struct GmmaCrossover {
    gmma: Gmma,
    prev_short_above: Option<bool>,
}

impl GmmaCrossover {
    pub fn new() -> Self {
        Self {
            gmma: Gmma::default(),
            prev_short_above: None,
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

        let short_avg: f64 = v.short.iter().sum::<f64>() / 6.0;
        let long_avg: f64  = v.long.iter().sum::<f64>()  / 6.0;
        let short_above = short_avg > long_avg;

        let prev = self.prev_short_above.replace(short_above);
        let Some(was_above) = prev else {
            return vec![];
        };

        if !was_above && short_above {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if was_above && !short_above {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "gmma_crossover"
    }

    fn reset(&mut self) {
        self.gmma = Gmma::default();
        self.prev_short_above = None;
    }
}
