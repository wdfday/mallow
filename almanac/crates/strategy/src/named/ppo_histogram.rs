use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Ppo;

/// Percentage Price Oscillator histogram zero-cross.
///
/// PPO normalises MACD to percentage — comparable across symbols with different price scales.
///
/// Long when PPO histogram crosses above zero (momentum turning positive).
/// Close when PPO histogram crosses below zero.
pub struct PpoHistogram {
    ppo: Ppo,
    prev_hist: Option<f64>,
    fast: usize,
    slow: usize,
    signal: usize,
}

impl PpoHistogram {
    pub fn new(fast: usize, slow: usize, signal: usize) -> Self {
        Self {
            ppo: Ppo::new(fast, slow, signal),
            prev_hist: None,
            fast,
            slow,
            signal,
        }
    }
}

impl Strategy for PpoHistogram {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(pv) = self.ppo.update(bar.close) else {
            return vec![];
        };

        let Some(prev) = self.prev_hist else {
            self.prev_hist = Some(pv.histogram);
            return vec![];
        };

        let crossed_above = prev <= 0.0 && pv.histogram > 0.0;
        let crossed_below = prev >= 0.0 && pv.histogram < 0.0;
        self.prev_hist = Some(pv.histogram);

        if crossed_above {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "ppo_histogram"
    }

    fn reset(&mut self) {
        self.ppo = Ppo::new(self.fast, self.slow, self.signal);
        self.prev_hist = None;
    }
}
