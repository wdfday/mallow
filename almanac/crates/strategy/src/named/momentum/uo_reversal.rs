use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Uo;

/// Ultimate Oscillator (Larry Williams) oversold reversal.
///
/// Long when UO crosses above the oversold level.
/// Close when UO reaches overbought level.
pub struct UoReversal {
    uo: Uo,
    prev_uo: Option<f64>,
    fast: usize,
    medium: usize,
    slow: usize,
    oversold: f64,
    overbought: f64,
}

impl UoReversal {
    pub fn new(fast: usize, medium: usize, slow: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            uo: Uo::new(fast, medium, slow),
            prev_uo: None,
            fast,
            medium,
            slow,
            oversold,
            overbought,
        }
    }
}

impl Strategy for UoReversal {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(uo) = self.uo.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let Some(prev) = self.prev_uo else {
            self.prev_uo = Some(uo);
            return vec![];
        };

        let crossed_oversold_up = prev <= self.oversold && uo > self.oversold;
        self.prev_uo = Some(uo);

        if crossed_oversold_up {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if uo > self.overbought {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "uo_reversal"
    }

    fn reset(&mut self) {
        self.uo = Uo::new(self.fast, self.medium, self.slow);
        self.prev_uo = None;
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

        let mut named = UoReversal::new(7, 14, 28, 30.0, 70.0);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let uo = ind.uo(0);
if uo[1] <= 30.0 && uo[0] > 30.0 { entry = true; }
if uo[0] > 70.0 { exit = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "uo_reversal: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
