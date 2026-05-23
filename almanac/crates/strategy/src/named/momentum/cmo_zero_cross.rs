use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Cmo, Ema};

/// Chande Momentum Oscillator zero-line cross + EMA trend filter.
///
/// Long when CMO crosses above zero (positive momentum) and price > EMA.
/// Close when CMO crosses below zero or price falls below EMA.
pub struct CmoZeroCross {
    cmo: Cmo,
    ema: Ema,
    prev_cmo: Option<f64>,
    cmo_period: usize,
    ema_period: usize,
}

impl CmoZeroCross {
    pub fn new(cmo_period: usize, ema_period: usize) -> Self {
        Self {
            cmo: Cmo::new(cmo_period),
            ema: Ema::new(ema_period),
            prev_cmo: None,
            cmo_period,
            ema_period,
        }
    }
}

impl Strategy for CmoZeroCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let cmo = self.cmo.update(bar.close);
        let ema = self.ema.update(bar.close);

        let Some(cmo) = cmo else { return vec![]; };
        let prev = self.prev_cmo.replace(cmo);
        let (Some(ema), Some(prev)) = (ema, prev) else { return vec![]; };

        let crossed_above_zero = prev <= 0.0 && cmo > 0.0;
        let crossed_below_zero = prev >= 0.0 && cmo < 0.0;

        if crossed_above_zero && bar.close > ema {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if crossed_below_zero || bar.close < ema {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "cmo_zero_cross"
    }

    fn reset(&mut self) {
        self.cmo = Cmo::new(self.cmo_period);
        self.ema = Ema::new(self.ema_period);
        self.prev_cmo = None;
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
        let bars = trending_bars(300);

        let mut named = CmoZeroCross::new(14, 50);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let cmo14 = ind.cmo(14, buf=2);
let ema50 = ind.ema(50, buf=1);
if cmo14[1] <= 0.0 && cmo14[0] > 0.0 && close[0] > ema50[0] { entry = true; }
if (cmo14[1] >= 0.0 && cmo14[0] < 0.0) || close[0] < ema50[0] { exit = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "cmo_zero_cross: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
