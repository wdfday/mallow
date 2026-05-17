use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Ema, Roc};

const RHAI_ROC: &str = r#"
let roc10 = ind.roc(10, 1);
let ema50  = ind.ema(50, 1);
if roc10[0] > 2.0 && close[0] > ema50[0] { entry = true; }
if roc10[0] < 0.0 || close[0] < ema50[0] { exit  = true; }
"#;

const RHAI_DUAL: &str = r#"
let roc10 = ind.roc(10, 1);
let roc30 = ind.roc(30, 1);
if roc10[0] > 0.0 && roc30[0] > 0.0 { entry = true; }
if roc10[0] < 0.0 || roc30[0] < 0.0 { exit  = true; }
"#;

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
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "momentum_roc"
    }

    fn description(&self) -> &'static str {
        "Long when ROC > entry threshold and price is above EMA. Exit when momentum fades or price drops below EMA."
    }

    fn script(&self) -> Option<&'static str> { Some(RHAI_ROC) }

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
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "dual_momentum"
    }

    fn description(&self) -> &'static str {
        "Long when both fast and slow ROC are positive. Exit when either turns negative."
    }

    fn script(&self) -> Option<&'static str> { Some(RHAI_DUAL) }

    fn reset(&mut self) {
        self.fast = Roc::new(self.fast_p);
        self.slow = Roc::new(self.slow_p);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    #[test]
    fn momentum_roc_script_parity() {
        // Fires every bar where ROC > 2.0 AND close > EMA(50); exit when either fails.
        let bars = trending_bars(300);

        let mut named = MomentumRoc::new(10, 50, 2.0, 0.0);
        let named_sigs = run(&mut named, &bars);

        let script = MomentumRoc::new(10, 50, 2.0, 0.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "momentum_roc: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }

    #[test]
    fn dual_momentum_script_parity() {
        // Fires every bar when both ROC(10) > 0 AND ROC(30) > 0.
        let bars = trending_bars(300);

        let mut named = DualMomentum::new(10, 30);
        let named_sigs = run(&mut named, &bars);

        let script = DualMomentum::new(10, 30).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "dual_momentum: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
