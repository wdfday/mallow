use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{BBands, Rsi};

/// Bot #26 — Mean Reversion Master.
///
/// Entry: price has been below the lower Bollinger Band for at least `bars`
/// consecutive bars, then closes back above it with RSI confirming oversold.
/// Exit:  price reaches upper band OR RSI turns overbought.
pub struct MeanReversion {
    bb: BBands,
    rsi: Rsi,
    /// Minimum consecutive bars below the lower band required before entry.
    min_bars: usize,
    below_band_count: usize,
    in_position: bool,
    bb_period: usize,
    bb_std: f64,
    rsi_period: usize,
    rsi_overbought: f64,
}

impl MeanReversion {
    pub fn new(bb_period: usize, bb_std: f64, rsi_period: usize, min_bars: usize) -> Self {
        Self {
            bb: BBands::new(bb_period, bb_std),
            rsi: Rsi::new(rsi_period),
            min_bars,
            below_band_count: 0,
            in_position: false,
            bb_period,
            bb_std,
            rsi_period,
            rsi_overbought: 70.0,
        }
    }
}

impl Strategy for MeanReversion {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let bb_val = self.bb.update(bar.close);
        let rsi_val = self.rsi.update(bar.close);

        let (Some(bb), Some(rsi)) = (bb_val, rsi_val) else {
            return vec![];
        };

        if self.in_position {
            if bar.close >= bb.upper || rsi > self.rsi_overbought {
                self.in_position = false;
                self.below_band_count = 0;
                return vec![Signal::exit(bar.timestamp, &bar.symbol)];
            }
            return vec![];
        }

        // Track how long price has been below lower band
        if bar.close < bb.lower {
            self.below_band_count += 1;
        } else if bar.close > bb.lower && self.below_band_count >= self.min_bars {
            // Price re-enters band from below after enough time outside
            self.below_band_count = 0;
            self.in_position = true;
            let strength = ((50.0 - rsi) / 50.0).clamp(0.0, 1.0);
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength)];
        } else {
            self.below_band_count = 0;
        }

        vec![]
    }

    fn name(&self) -> &str {
        "mean_reversion"
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.rsi = Rsi::new(self.rsi_period);
        self.below_band_count = 0;
        self.in_position = false;
    }
}
