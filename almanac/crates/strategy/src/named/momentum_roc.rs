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
    in_position: bool,
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
            in_position: false,
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

        if r > self.entry_threshold && above_trend && !self.in_position {
            self.in_position = true;
            let strength = (r / (self.entry_threshold * 3.0)).clamp(0.0, 1.0);
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength)];
        }

        if (r < self.exit_threshold || !above_trend) && self.in_position {
            self.in_position = false;
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
        self.in_position = false;
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
    in_position: bool,
}

impl DualMomentum {
    pub fn new(fast_period: usize, slow_period: usize) -> Self {
        Self {
            fast: Roc::new(fast_period),
            slow: Roc::new(slow_period),
            fast_p: fast_period,
            slow_p: slow_period,
            in_position: false,
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

        if f > 0.0 && s > 0.0 && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if (f < 0.0 || s < 0.0) && self.in_position {
            self.in_position = false;
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
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn momentum_roc_parity() {
        let bars = trending_bars(300);

        let mut hc = MomentumRoc::new(10, 50, 2.0, 0.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "roc": { "type": "roc", "period": 10 },
                "ema": { "type": "ema", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "roc",   "field": "value", "op": "gt", "value": 2.0 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ema" }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "roc",   "field": "value", "op": "lt", "value": 0.0 },
                { "source": "close", "field": "value", "op": "lt", "compare": "ema" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "roc(10) > 2.0 && close > ema(50)",
            "exit":  "roc(10) < 0.0 || close < ema(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "momentum_roc: no signals");
        assert_parity("momentum_roc hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("momentum_roc hc vs cel",     &hc_sigs, &cel_sigs);
    }

    #[test]
    fn dual_momentum_parity() {
        let bars = trending_bars(300);

        let mut hc = DualMomentum::new(10, 30);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "roc", "period": 10 },
                "slow": { "type": "roc", "period": 30 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "fast", "field": "value", "op": "gt", "value": 0.0 },
                { "source": "slow", "field": "value", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "fast", "field": "value", "op": "lt", "value": 0.0 },
                { "source": "slow", "field": "value", "op": "lt", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "roc(10) > 0.0 && roc(30) > 0.0",
            "exit":  "roc(10) < 0.0 || roc(30) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "dual_momentum: no signals");
        assert_parity("dual_momentum hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("dual_momentum hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
