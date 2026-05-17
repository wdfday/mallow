use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Sma;

/// Bot #45 — 50 MA Pull Back.
///
/// Trades pullbacks in an uptrend:
/// 1. Price must be in an uptrend (above MA).
/// 2. A pullback occurs: price falls within `proximity_pct` of the MA.
/// 3. Entry: price bounces back up away from the MA.
/// 4. Exit: price falls below the MA.
pub struct MaPullback {
    ma: Sma,
    /// Fraction of MA value — price is "near MA" when within this distance.
    proximity_pct: f64,
    near_ma: bool,
    in_position: bool,
    ma_period: usize,
}

impl MaPullback {
    pub fn new(ma_period: usize) -> Self {
        Self {
            ma: Sma::new(ma_period),
            proximity_pct: 0.02, // 2% proximity threshold
            near_ma: false,
            in_position: false,
            ma_period,
        }
    }
}

impl Strategy for MaPullback {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(ma) = self.ma.update(bar.close) else {
            return vec![];
        };

        if self.in_position {
            if bar.close < ma {
                self.in_position = false;
                self.near_ma = false;
                return vec![Signal::exit(bar.timestamp, &bar.symbol)];
            }
            return vec![];
        }

        // Only consider pullbacks when price is above MA (uptrend)
        if bar.close < ma {
            self.near_ma = false;
            return vec![];
        }

        let proximity = (bar.close - ma) / ma;

        if proximity <= self.proximity_pct {
            // Price pulled back to near the MA
            self.near_ma = true;
        } else if self.near_ma && proximity > self.proximity_pct {
            // Price bouncing away from MA after pullback
            self.near_ma = false;
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "ma_pullback"
    }

    fn reset(&mut self) {
        self.ma = Sma::new(self.ma_period);
        self.near_ma = false;
        self.in_position = false;
    }
}
