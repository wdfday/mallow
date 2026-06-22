use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{BBands, Keltner};

pub(crate) const RHAI_SCRIPT: &str = r#"
let bb20 = ind.bbands(20);
let kc20 = ind.keltner(20, 10, multiplier=1.5);
let squeezed = bb20[0].upper < kc20[0].upper && bb20[0].lower > kc20[0].lower;
if state["was_squeezed"] == () {
    state["was_squeezed"] = false;
    state["in_position"] = false;
}
let squeeze_released = state["was_squeezed"] && !squeezed;
state["was_squeezed"] = squeezed;
if squeeze_released && close[0] > bb20[0].middle && !state["in_position"] {
    state["in_position"] = true;
    entry = true;
}
if close[0] < bb20[0].middle && state["in_position"] {
    state["in_position"] = false;
    exit = true;
}
"#;

/// Bot — Bollinger Bands inside Keltner Channel squeeze.
///
/// Long on squeeze release when close > BB middle.
/// Close when close < BB middle.
pub struct BbKeltnerSqueeze {
    bb: BBands,
    keltner: Keltner,
    was_squeezed: bool,
    in_position: bool,
    bb_period: usize,
    bb_std: f64,
    keltner_period: usize,
    keltner_atr: usize,
    keltner_mult: f64,
}

impl BbKeltnerSqueeze {
    pub fn new(
        bb_period: usize,
        bb_std: f64,
        keltner_period: usize,
        keltner_atr: usize,
        keltner_mult: f64,
    ) -> Self {
        Self {
            bb: BBands::new(bb_period, bb_std),
            keltner: Keltner::new(keltner_period, keltner_atr, keltner_mult),
            was_squeezed: false,
            in_position: false,
            bb_period,
            bb_std,
            keltner_period,
            keltner_atr,
            keltner_mult,
        }
    }
}

impl Strategy for BbKeltnerSqueeze {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let bb = self.bb.update(bar.close);
        let kc = self.keltner.update(bar.high, bar.low, bar.close);

        let (Some(bb), Some(kc)) = (bb, kc) else {
            return vec![];
        };

        let squeezed = bb.upper < kc.upper && bb.lower > kc.lower;
        let squeeze_released = self.was_squeezed && !squeezed;
        self.was_squeezed = squeezed;

        if squeeze_released && bar.close > bb.middle && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < bb.middle && self.in_position {
            self.in_position = false;
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "bb_keltner_squeeze"
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.keltner = Keltner::new(self.keltner_period, self.keltner_atr, self.keltner_mult);
        self.was_squeezed = false;
        self.in_position = false;
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
    fn bb_keltner_squeeze_script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = BbKeltnerSqueeze::new(20, 2.0, 20, 10, 1.5);
        let named_sigs = run(&mut named, &bars);

        let script = BbKeltnerSqueeze::new(20, 2.0, 20, 10, 1.5).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "bb_keltner_squeeze: must produce signals");
        assert_eq!(named_sigs, script_sigs, "bb_keltner_squeeze script parity failed");
    }
}
