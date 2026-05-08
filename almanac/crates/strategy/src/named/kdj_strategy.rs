//! KDJ Strategy
//!
//! Long when K and D both cross below oversold and J turns up.
//! Exit when K crosses above overbought or J exceeds 100.

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Kdj;

pub struct KdjStrategy {
    kdj: Kdj,
    oversold: f64,
    overbought: f64,
    prev_k: Option<f64>,
    in_position: bool,
}

impl KdjStrategy {
    pub fn new(period: usize, k_period: usize, d_period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            kdj: Kdj::new(period, k_period, d_period),
            oversold,
            overbought,
            prev_k: None,
            in_position: false,
        }
    }
}

impl Strategy for KdjStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.kdj.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let mut signals = vec![];

        if !self.in_position {
            // Entry: K and D both below oversold, J starting to turn up
            if let Some(pk) = self.prev_k {
                if v.k < self.oversold && v.d < self.oversold && v.k > pk {
                    self.in_position = true;
                    signals.push(Signal::long(bar.timestamp, &bar.symbol, 1.0));
                }
            }
        } else {
            // Exit: K above overbought or J exceeds 100 (exhaustion)
            if v.k > self.overbought || v.j > 100.0 {
                self.in_position = false;
                signals.push(Signal::close(bar.timestamp, &bar.symbol));
            }
        }

        self.prev_k = Some(v.k);
        signals
    }

    fn name(&self) -> &str { "kdj" }

    fn reset(&mut self) {
        self.kdj.reset();
        self.prev_k = None;
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;
    use crate::test_utils::*;

    #[test]
    fn kdj_hc_produces_signals() {
        let warmup: Vec<Bar> = (0..40).map(|i| bar(i as i64 * 60_000, 100.0)).collect();
        let offset = warmup.len() as i64 * 60_000;
        let v_shape: Vec<Bar> = rsi_bars(200)
            .into_iter()
            .map(|b| bar(b.timestamp + offset, b.close))
            .collect();
        let bars: Vec<Bar> = warmup.into_iter().chain(v_shape).collect();

        let mut hc = KdjStrategy::new(9, 3, 3, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);
        assert!(!hc_sigs.is_empty(), "kdj: no signals");
    }

}
