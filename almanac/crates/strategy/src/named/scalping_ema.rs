use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Atr, Ema};

/// Scalping EMA — fast/slow EMA cross confirmed by ATR expansion.
///
/// Long  when fast EMA crosses above slow EMA AND ATR > ATR MA (momentum expanding).
/// Close when fast EMA crosses below slow EMA.
///
/// Default: fast=8, slow=21, atr=14, atr_ma=20
pub struct ScalpingEma {
    fast: Ema,
    slow: Ema,
    atr: Atr,
    atr_ma: Ema,
    fast_p: usize,
    slow_p: usize,
    atr_p: usize,
    atr_ma_p: usize,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
}

impl ScalpingEma {
    pub fn new(fast: usize, slow: usize, atr_period: usize, atr_ma_period: usize) -> Self {
        Self {
            fast: Ema::new(fast),
            slow: Ema::new(slow),
            atr: Atr::new(atr_period),
            atr_ma: Ema::new(atr_ma_period),
            fast_p: fast,
            slow_p: slow,
            atr_p: atr_period,
            atr_ma_p: atr_ma_period,
            prev_fast: None,
            prev_slow: None,
        }
    }
}

impl Strategy for ScalpingEma {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);
        let atr_val = self.atr.update(bar.high, bar.low, bar.close);

        let atr_ma = atr_val.and_then(|a| self.atr_ma.update(a.atr));

        let (Some(f), Some(s), Some(a), Some(a_ma)) = (fast, slow, atr_val, atr_ma) else {
            self.prev_fast = fast;
            self.prev_slow = slow;
            return vec![];
        };

        let prev_f = self.prev_fast.replace(f);
        let prev_s = self.prev_slow.replace(s);

        let (Some(pf), Some(ps)) = (prev_f, prev_s) else {
            return vec![];
        };

        let atr_expanding = a.atr > a_ma;

        // Bullish cross: fast crosses above slow
        if pf <= ps && f > s && atr_expanding {
            let strength = ((f - s) / s).clamp(0.0, 1.0);
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength)];
        }

        // Bearish cross: fast crosses below slow
        if pf >= ps && f < s {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "scalping_ema"
    }

    fn reset(&mut self) {
        self.fast = Ema::new(self.fast_p);
        self.slow = Ema::new(self.slow_p);
        self.atr = Atr::new(self.atr_p);
        self.atr_ma = Ema::new(self.atr_ma_p);
        self.prev_fast = None;
        self.prev_slow = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn bar(ts: i64, c: f64) -> Bar {
        Bar::new(ts, "T", c, c * 1.01, c * 0.99, c, 1000.0)
    }

    #[test]
    fn test_scalping_ema_no_signal_before_warmup() {
        let mut s = ScalpingEma::new(8, 21, 14, 20);
        for i in 0..20 {
            assert!(s.on_bar(&bar(i, 100.0)).is_empty());
        }
    }

    #[test]
    fn test_scalping_ema_long_on_cross() {
        let mut s = ScalpingEma::new(3, 7, 3, 5);
        // Downtrend first (so fast < slow)
        for i in 0..10 { s.on_bar(&bar(i, 100.0 - i as f64)); }
        // Sharp upturn to force cross
        let mut sigs = vec![];
        for i in 10..30 { sigs.extend(s.on_bar(&bar(i, 80.0 + (i - 10) as f64 * 5.0))); }
        use alm_core::signal::Direction;
        assert!(sigs.iter().any(|s| s.direction == Direction::Long));
    }
}
