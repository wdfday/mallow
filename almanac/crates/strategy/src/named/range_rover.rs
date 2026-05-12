use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Sma, Stochastic};

/// Bot #30 — Range Rover.
///
/// Enters long when stochastic %K is in oversold zone AND price is above MA
/// (staying in the broader uptrend).  Exits when %K reaches overbought.
pub struct RangeRover {
    stoch: Stochastic,
    ma: Sma,
    oversold: f64,
    overbought: f64,
    k_period: usize,
    d_period: usize,
    ma_period: usize,
}

impl RangeRover {
    pub fn new(k_period: usize, d_period: usize, ma_period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            stoch: Stochastic::new(k_period, d_period),
            ma: Sma::new(ma_period),
            oversold,
            overbought,
            k_period,
            d_period,
            ma_period,
        }
    }
}

impl Strategy for RangeRover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let stoch_val = self.stoch.update(bar.high, bar.low, bar.close);
        let ma_val = self.ma.update(bar.close);

        let (Some(st), Some(ma)) = (stoch_val, ma_val) else {
            return vec![];
        };

        if st.k < self.oversold && bar.close > ma {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if st.k > self.overbought {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "range_rover"
    }

    fn reset(&mut self) {
        self.stoch = Stochastic::new(self.k_period, self.d_period);
        self.ma = Sma::new(self.ma_period);
    }
}
