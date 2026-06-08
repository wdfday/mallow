use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Adx, Ema};

/// EMA crossover gated by ADX trending strength.
///
/// Long when fast EMA crosses above slow EMA AND ADX > threshold (confirmed trend).
/// Close when fast EMA crosses below slow EMA.
pub struct AdxEmaCross {
    fast: Ema,
    slow: Ema,
    adx: Adx,
    prev_fast: Option<f64>,
    prev_slow: Option<f64>,
    fast_period: usize,
    slow_period: usize,
    adx_period: usize,
    adx_threshold: f64,
}

impl AdxEmaCross {
    pub fn new(
        fast_period: usize,
        slow_period: usize,
        adx_period: usize,
        adx_threshold: f64,
    ) -> Self {
        Self {
            fast: Ema::new(fast_period),
            slow: Ema::new(slow_period),
            adx: Adx::new(adx_period),
            prev_fast: None,
            prev_slow: None,
            fast_period,
            slow_period,
            adx_period,
            adx_threshold,
        }
    }
}

impl Strategy for AdxEmaCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let fast = self.fast.update(bar.close);
        let slow = self.slow.update(bar.close);
        let adx_val = self.adx.update(bar.high, bar.low, bar.close);

        let (Some(f), Some(s), Some(adx)) = (fast, slow, adx_val) else {
            return vec![];
        };

        let (Some(pf), Some(ps)) = (self.prev_fast, self.prev_slow) else {
            self.prev_fast = Some(f);
            self.prev_slow = Some(s);
            return vec![];
        };

        let crossed_above = pf <= ps && f > s;
        let crossed_below = pf >= ps && f < s;
        self.prev_fast = Some(f);
        self.prev_slow = Some(s);

        if crossed_above && adx.adx > self.adx_threshold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "adx_ema_cross"
    }

    fn reset(&mut self) {
        self.fast = Ema::new(self.fast_period);
        self.slow = Ema::new(self.slow_period);
        self.adx = Adx::new(self.adx_period);
        self.prev_fast = None;
        self.prev_slow = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = AdxEmaCross::new(20, 50, 14, 25.0);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let e20 = ind.ema(20);
let e50 = ind.ema(50);
let adx14 = ind.adx(14, buf=1);
if cross_above(e20, e50) && adx14[0] > 25.0 { entry = true; }
if cross_below(e20, e50) { exit = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "adx_ema_cross: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
