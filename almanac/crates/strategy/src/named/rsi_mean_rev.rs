use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Rsi;

/// RSI Mean Reversion strategy.
/// Buy when RSI drops below `oversold`; sell/close when RSI rises above `overbought`.
pub struct RsiMeanRev {
    rsi: Rsi,
    oversold: f64,
    overbought: f64,
    in_position: bool,
    period: usize,
}

impl RsiMeanRev {
    pub fn new(period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            rsi: Rsi::new(period),
            oversold,
            overbought,
            in_position: false,
            period,
        }
    }
}

impl Strategy for RsiMeanRev {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(rsi) = self.rsi.update(bar.close) else {
            return vec![];
        };

        if rsi < self.oversold && !self.in_position {
            self.in_position = true;
            let strength = (self.oversold - rsi) / self.oversold;
            return vec![Signal::long(
                bar.timestamp,
                &bar.symbol,
                strength.clamp(0.0, 1.0),
            )];
        }

        if rsi > self.overbought && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "rsi_mean_rev"
    }

    fn reset(&mut self) {
        self.rsi = Rsi::new(self.period);
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;

    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "TEST", close, close + 1.0, close - 1.0, close, 1000.0)
    }

    #[test]
    fn test_no_signal_before_rsi_ready() {
        let mut strat = RsiMeanRev::new(14, 30.0, 70.0);
        for i in 0..14 {
            let s = strat.on_bar(&bar(i, 100.0));
            assert!(s.is_empty(), "no signal before RSI ready");
        }
    }

    #[test]
    fn test_long_signal_on_oversold() {
        let mut strat = RsiMeanRev::new(14, 30.0, 70.0);
        // Feed descending prices to push RSI below 30
        let mut signals = Vec::new();
        for i in 0..25 {
            let price = 150.0 - i as f64 * 3.0;
            signals.extend(strat.on_bar(&bar(i, price.max(1.0))));
        }
        use alm_core::signal::Direction;
        assert!(
            signals.iter().any(|s| s.direction == Direction::Long),
            "should emit Long when RSI oversold"
        );
    }

    #[test]
    fn test_close_signal_on_overbought() {
        let mut strat = RsiMeanRev::new(14, 30.0, 70.0);
        let mut signals = Vec::new();
        // Push RSI oversold → get Long
        for i in 0..25 {
            signals.extend(strat.on_bar(&bar(i, 100.0 - i as f64 * 2.0)));
        }
        // Push RSI overbought → get Close
        for i in 25..55 {
            signals.extend(strat.on_bar(&bar(i, 50.0 + (i - 25) as f64 * 3.0)));
        }
        use alm_core::signal::Direction;
        assert!(
            signals.iter().any(|s| s.direction == Direction::Close),
            "should emit Close when RSI overbought"
        );
    }
}
