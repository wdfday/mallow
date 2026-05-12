use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Atr, BBands};

/// Bot #38 — Volatility Vanguard.
///
/// Long when price breaks above the upper Bollinger Band AND ATR is expanding
/// (confirming the move has real momentum behind it).
/// Closes when price falls back below the middle band.
pub struct VolatilityVanguard {
    bb: BBands,
    atr: Atr,
    prev_atr: Option<f64>,
    bb_period: usize,
    bb_std: f64,
    atr_period: usize,
}

impl VolatilityVanguard {
    pub fn new(bb_period: usize, bb_std: f64, atr_period: usize) -> Self {
        Self {
            bb: BBands::new(bb_period, bb_std),
            atr: Atr::new(atr_period),
            prev_atr: None,
            bb_period,
            bb_std,
            atr_period,
        }
    }
}

impl Strategy for VolatilityVanguard {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let bb_val = self.bb.update(bar.close);
        let atr_val = self.atr.update(bar.high, bar.low, bar.close);

        let (Some(bb), Some(atr)) = (bb_val, atr_val) else {
            return vec![];
        };

        let Some(prev_atr) = self.prev_atr else {
            self.prev_atr = Some(atr.atr);
            return vec![];
        };

        let atr_expanding = atr.atr > prev_atr;
        self.prev_atr = Some(atr.atr);

        if bar.close > bb.upper && atr_expanding {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < bb.middle {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "volatility_vanguard"
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.atr = Atr::new(self.atr_period);
        self.prev_atr = None;
    }
}
