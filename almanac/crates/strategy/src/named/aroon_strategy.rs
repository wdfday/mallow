use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Aroon;

/// Aroon Trend — enters when Aroon Up dominates, exits when direction reverses.
///
/// Long  when Aroon Up > `bull_threshold` AND Aroon Down < `bear_threshold`.
/// Close when Aroon Up < Aroon Down (trend reversal confirmed).
///
/// Default: period=25, bull_threshold=70, bear_threshold=30
pub struct AroonTrend {
    aroon: Aroon,
    period: usize,
    bull_threshold: f64,
    bear_threshold: f64,
    in_position: bool,
}

impl AroonTrend {
    pub fn new(period: usize, bull_threshold: f64, bear_threshold: f64) -> Self {
        Self {
            aroon: Aroon::new(period),
            period,
            bull_threshold,
            bear_threshold,
            in_position: false,
        }
    }
}

impl Strategy for AroonTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.aroon.update(bar.high, bar.low) else {
            return vec![];
        };

        if v.up > self.bull_threshold && v.down < self.bear_threshold && !self.in_position {
            self.in_position = true;
            let strength = (v.up - v.down) / 200.0 + 0.5;
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength.clamp(0.0, 1.0))];
        }

        // Exit when aroon up drops below down (trend lost)
        if v.up < v.down && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "aroon_trend"
    }

    fn reset(&mut self) {
        self.aroon = Aroon::new(self.period);
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn bar(ts: i64, h: f64, l: f64) -> Bar {
        Bar::new(ts, "T", h, h, l, (h + l) / 2.0, 1000.0)
    }

    #[test]
    fn test_aroon_no_signal_warmup() {
        let mut s = AroonTrend::new(25, 70.0, 30.0);
        for i in 0..25 { assert!(s.on_bar(&bar(i, 100.0 + i as f64, 99.0)).is_empty()); }
    }
}
