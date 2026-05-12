use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Stochastic;

/// Bot #48 — Stochastic %D/%K crossover.
///
/// Pure crossover — no overbought/oversold zone restriction.
/// Long when %K crosses above %D; closes when %K crosses below %D.
pub struct StochasticDk {
    stoch: Stochastic,
    prev_k: Option<f64>,
    prev_d: Option<f64>,
    k_period: usize,
    d_period: usize,
}

impl StochasticDk {
    pub fn new(k_period: usize, d_period: usize) -> Self {
        Self {
            stoch: Stochastic::new(k_period, d_period),
            prev_k: None,
            prev_d: None,
            k_period,
            d_period,
        }
    }
}

impl Strategy for StochasticDk {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.stoch.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let (Some(pk), Some(pd)) = (self.prev_k, self.prev_d) else {
            self.prev_k = Some(v.k);
            self.prev_d = Some(v.d);
            return vec![];
        };

        let crossed_up = pk <= pd && v.k > v.d;
        let crossed_down = pk >= pd && v.k < v.d;

        self.prev_k = Some(v.k);
        self.prev_d = Some(v.d);

        if crossed_up  {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_down {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "stochastic_dk"
    }

    fn reset(&mut self) {
        self.stoch = Stochastic::new(self.k_period, self.d_period);
        self.prev_k = None;
        self.prev_d = None;
    }
}
