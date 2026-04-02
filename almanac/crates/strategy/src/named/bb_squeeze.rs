use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::BBands;

/// Bot #42 — Bollinger Band Squeeze.
///
/// Detects when the bands narrow (low volatility / squeeze) then waits for
/// a breakout above the upper band.  This combination is classic — the squeeze
/// coils energy that is released explosively.
///
/// Squeeze threshold: bandwidth (upper−lower)/middle < `squeeze_threshold`.
pub struct BbSqueeze {
    bb: BBands,
    squeeze_threshold: f64,
    was_squeezed: bool,
    in_position: bool,
    period: usize,
    std: f64,
}

impl BbSqueeze {
    pub fn new(period: usize, std: f64) -> Self {
        // Squeeze when band width < 4% of price — empirically reasonable default
        Self {
            bb: BBands::new(period, std),
            squeeze_threshold: 0.04,
            was_squeezed: false,
            in_position: false,
            period,
            std,
        }
    }

    pub fn with_squeeze_threshold(mut self, threshold: f64) -> Self {
        self.squeeze_threshold = threshold;
        self
    }
}

impl Strategy for BbSqueeze {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(bb) = self.bb.update(bar.close) else {
            return vec![];
        };

        let squeezed = bb.bandwidth < self.squeeze_threshold;

        if squeezed {
            self.was_squeezed = true;
        }

        if !self.in_position && self.was_squeezed && bar.close > bb.upper {
            self.in_position = true;
            self.was_squeezed = false;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && bar.close < bb.middle {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "bb_squeeze"
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.period, self.std);
        self.was_squeezed = false;
        self.in_position = false;
    }
}
