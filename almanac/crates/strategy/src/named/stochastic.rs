use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Stochastic, StochasticValue};

/// Stochastic Crossover strategy.
///
/// Buy when %K crosses above %D in oversold zone (< 20).
/// Sell when %K crosses below %D in overbought zone (> 80).
pub struct StochasticCrossover {
    stoch: Stochastic,
    prev: Option<StochasticValue>,
    in_position: bool,
    k_period: usize,
    d_period: usize,
    oversold: f64,
    overbought: f64,
}

impl StochasticCrossover {
    pub fn new(k_period: usize, d_period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            stoch: Stochastic::new(k_period, d_period),
            prev: None,
            in_position: false,
            k_period,
            d_period,
            oversold,
            overbought,
        }
    }

    /// Standard Stochastic(14, 3) with 20/80 thresholds
    pub fn standard() -> Self {
        Self::new(14, 3, 20.0, 80.0)
    }
}

impl Strategy for StochasticCrossover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(curr) = self.stoch.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let Some(prev) = self.prev else {
            self.prev = Some(curr);
            return vec![];
        };

        let result = {
            // %K crosses above %D in oversold zone → buy
            let k_crossed_above = prev.k <= prev.d && curr.k > curr.d;
            let in_oversold = curr.d < self.oversold;

            // %K crosses below %D in overbought zone → sell
            let k_crossed_below = prev.k >= prev.d && curr.k < curr.d;
            let in_overbought = curr.d > self.overbought;

            if k_crossed_above && in_oversold && !self.in_position {
                self.in_position = true;
                vec![Signal::long(bar.timestamp, &bar.symbol, curr.k / 100.0)]
            } else if k_crossed_below && in_overbought && self.in_position {
                self.in_position = false;
                vec![Signal::close(bar.timestamp, &bar.symbol)]
            } else {
                vec![]
            }
        };

        self.prev = Some(curr);
        result
    }

    fn name(&self) -> &str {
        "stochastic_crossover"
    }

    fn reset(&mut self) {
        self.stoch = Stochastic::new(self.k_period, self.d_period);
        self.prev = None;
        self.in_position = false;
    }
}
