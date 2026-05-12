use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, Roc};

/// Momentum ROC — pure price momentum with trend filter.
///
/// Long  when ROC > `entry_threshold` (strong upward momentum) AND price above EMA.
/// Close when ROC < `exit_threshold` (momentum fades) OR price drops below EMA.
///
/// Default: roc=10, entry=2.0%, exit=0.0%, ema=50
pub struct MomentumRoc {
    roc: Roc,
    ema: Ema,
    roc_p: usize,
    ema_p: usize,
    entry_threshold: f64,
    exit_threshold: f64,
}

impl MomentumRoc {
    pub fn new(roc_period: usize, ema_period: usize, entry_threshold: f64, exit_threshold: f64) -> Self {
        Self {
            roc: Roc::new(roc_period),
            ema: Ema::new(ema_period),
            roc_p: roc_period,
            ema_p: ema_period,
            entry_threshold,
            exit_threshold,
        }
    }
}

impl Strategy for MomentumRoc {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let roc = self.roc.update(bar.close);
        let ema = self.ema.update(bar.close);

        let (Some(r), Some(e)) = (roc, ema) else {
            return vec![];
        };

        let above_trend = bar.close > e;

        if r > self.entry_threshold && above_trend {
            let strength = (r / (self.entry_threshold * 3.0)).clamp(0.0, 1.0);
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength)];
        }

        if r < self.exit_threshold || !above_trend {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "momentum_roc"
    }

    fn reset(&mut self) {
        self.roc = Roc::new(self.roc_p);
        self.ema = Ema::new(self.ema_p);
    }
}

/// Dual Momentum — absolute + relative momentum filter.
///
/// Long  when ROC(fast) > 0 AND ROC(slow) > 0 (both timeframes agree).
/// Close when either turns negative.
pub struct DualMomentum {
    fast: Roc,
    slow: Roc,
    fast_p: usize,
    slow_p: usize,
}

impl DualMomentum {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        Self {
            fast: Roc::new(fast_period),
            slow: Roc::new(slow_period),
            fast_p: fast_period,
            slow_p: slow_period,
        }
    }
}

impl Strategy for DualMomentum {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let f = self.fast.update(bar.close);
        let s = self.slow.update(bar.close);

        let (Some(f), Some(s)) = (f, s) else {
            return vec![];
        };

        if f > 0.0 && s > 0.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if f < 0.0 || s < 0.0 {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "dual_momentum"
    }

    fn reset(&mut self) {
        self.fast = Roc::new(self.fast_p);
        self.slow = Roc::new(self.slow_p);
    }
}
