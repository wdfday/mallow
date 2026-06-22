use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::BBands;

/// Bot #42 — Bollinger Band Squeeze.
///
/// Detects when the bands narrow (low volatility / squeeze) then waits for
/// a breakout above the upper band.  This combination is classic — the squeeze
/// coils energy that is released explosively.
///
/// Squeeze threshold: bandwidth (upper−lower)/middle < `squeeze_threshold`.
pub struct BbSqueeze {
    bb: BBands,
    squeeze_threshold: f64,
    was_squeezed: bool,
    period: usize,
    std: f64,
}

impl BbSqueeze {
    pub fn new(period: usize, std: f64) -> Self {
        // Squeeze when band width < 4% of price — empirically reasonable default
        Self {
            bb: BBands::new(period, std),
            squeeze_threshold: 0.04,
            was_squeezed: false,
            period,
            std,
        }
    }

    pub fn with_squeeze_threshold(mut self, threshold: f64) -> Self {
        self.squeeze_threshold = threshold;
        self
    }
}

impl Strategy for BbSqueeze {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(bb) = self.bb.update(bar.close) else {
            return vec![];
        };

        let squeezed = bb.bandwidth < self.squeeze_threshold;

        if squeezed {
            self.was_squeezed = true;
        }

        if self.was_squeezed && bar.close > bb.upper {
            self.was_squeezed = false;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < bb.middle {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "bb_squeeze"
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.period, self.std);
        self.was_squeezed = false;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let bb20 = ind.bbands(20);
let squeezed = bb20[0].bandwidth < 0.04;
if state["was_squeezed"] == () {
    state["was_squeezed"] = false;
}
if squeezed {
    state["was_squeezed"] = true;
}
if state["was_squeezed"] && close[0] > bb20[0].upper {
    state["was_squeezed"] = false;
    entry = true;
}
if close[0] < bb20[0].middle {
    exit = true;
}
"#;
#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = BbSqueeze::new(20, 2.0);
        let named_sigs = run(&mut named, &bars);

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "bb_squeeze: must produce signals");
        assert_parity("bb_squeeze parity vs named", &named_sigs, &script_sigs);
    }
}
