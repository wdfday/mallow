use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Donchian;

/// Donchian Channel Breakout — classic trend-following system (Turtle Traders).
///
/// Long  when close breaks above N-bar high (upper channel).
/// Close when close drops below M-bar low (lower channel, typically M < N).
///
/// Default: entry=20, exit=10
pub struct DonchianBreakout {
    entry: Donchian,
    exit: Donchian,
    entry_p: usize,
    exit_p: usize,
    prev_upper: Option<f64>,
    prev_lower: Option<f64>,
    in_position: bool,
}

impl DonchianBreakout {
    pub fn new(entry_period: usize, exit_period: usize) -> Self {
        Self {
            entry: Donchian::new(entry_period),
            exit: Donchian::new(exit_period),
            entry_p: entry_period,
            exit_p: exit_period,
            prev_upper: None,
            prev_lower: None,
            in_position: false,
        }
    }
}

impl Strategy for DonchianBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let en = self.entry.update(bar.high, bar.low);
        let ex = self.exit.update(bar.high, bar.low);

        let (Some(en), Some(ex)) = (en, ex) else {
            return vec![];
        };

        // Use previous bar's channel to avoid look-ahead
        let prev_upper = self.prev_upper.replace(en.upper);
        let prev_lower = self.prev_lower.replace(ex.lower);

        let (Some(pu), Some(pl)) = (prev_upper, prev_lower) else {
            return vec![];
        };

        // Entry: close breaks above previous upper channel
        if bar.close > pu && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }

        // Exit: close drops below previous exit channel lower
        if bar.close < pl && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "donchian_breakout"
    }

    fn reset(&mut self) {
        self.entry = Donchian::new(self.entry_p);
        self.exit = Donchian::new(self.exit_p);
        self.prev_upper = None;
        self.prev_lower = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn bar(ts: i64, c: f64) -> Bar {
        Bar::new(ts, "T", c, c * 1.01, c * 0.99, c, 1000.0)
    }

    #[test]
    fn test_no_signal_before_warmup() {
        let mut s = DonchianBreakout::new(20, 10);
        for i in 0..20 { assert!(s.on_bar(&bar(i, 100.0)).is_empty()); }
    }

    #[test]
    fn test_breakout_long() {
        let mut s = DonchianBreakout::new(5, 3);
        // Flat market to establish channel
        for i in 0..5 { s.on_bar(&bar(i, 100.0)); }
        // Breakout above
        let sigs = s.on_bar(&bar(5, 110.0));
        use alm_core::signal::Direction;
        assert!(sigs.iter().any(|s| s.direction == Direction::Long));
    }
}
