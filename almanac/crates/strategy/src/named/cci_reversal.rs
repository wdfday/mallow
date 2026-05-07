use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Cci;

/// Bot — CCI Reversal.
///
/// Long when CCI crosses above `entry_level` from below (default −100).
/// Close when CCI crosses above `exit_level` (default +100).
pub struct CciReversal {
    cci: Cci,
    entry_level: f64,
    exit_level: f64,
    prev_cci: Option<f64>,
    in_position: bool,
    period: usize,
}

impl CciReversal {
    pub fn new(period: usize, entry_level: f64, exit_level: f64) -> Self {
        Self {
            cci: Cci::new(period),
            entry_level,
            exit_level,
            prev_cci: None,
            in_position: false,
            period,
        }
    }
}

impl Strategy for CciReversal {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.cci.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let prev = self.prev_cci.replace(v);
        let Some(p) = prev else {
            return vec![];
        };

        // Cross above entry_level (e.g. -100 → oversold recovery)
        if p <= self.entry_level && v > self.entry_level && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        // Cross above exit_level (e.g. +100 → overbought)
        if p <= self.exit_level && v > self.exit_level && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "cci_reversal"
    }

    fn reset(&mut self) {
        self.cci = Cci::new(self.period);
        self.prev_cci = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {

}
