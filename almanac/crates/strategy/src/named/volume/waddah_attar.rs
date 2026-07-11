use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Atr, BBands, Macd};

/// Bot #44 — Volume Explosion (Waddah Attar Explosion, WAE).
///
/// Canonical formula (LazyBear's widely-used port, the de-facto reference):
/// ```text
/// t1 (explosion)  = (macd_line[t] - macd_line[t-1]) × sensitivity   ← sign = trend direction
/// explosion_line  = BB(close, bb_period, bb_std).upper - .lower     ← "power" reference line
/// deadzone_line   = ATR(100, Wilder-smoothed) × 3.7                 ← chop/no-trade filter
///
/// confirmed = |t1| > explosion_line AND |t1| > deadzone_line
/// Long  when confirmed AND t1 > 0 (bullish explosion)
/// Exit  when NOT confirmed OR t1 <= 0 (explosion faded or flipped bearish)
/// ```
/// Direction comes from the **sign of `t1` itself**, not the MACD histogram — the
/// histogram (MACD − signal line) is a separate concept unrelated to WAE's own
/// decomposition. A signal only counts once it clears BOTH the BB-width power
/// line and the ATR-based deadzone — using only one (as an earlier revision of
/// this strategy did with BB-width alone) lets chop-market MACD wiggles slip
/// through as "explosions".
///
/// References: Waddah Attar Explosion — volume/momentum burst detector,
/// popularized on TradingView via LazyBear's `WAE_LB` script (fast=20, slow=40,
/// sensitivity=150, BB(20, 2.0), deadzone=ATR(100)×3.7).
pub struct WaddahAttar {
    macd: Macd,
    bb: BBands,
    atr: Atr,
    prev_macd_line: Option<f64>,
    sensitivity: f64,
    dz_multiplier: f64,
    dz_period: usize,
    fast: usize,
    slow: usize,
    signal_period: usize,
    bb_period: usize,
    bb_std: f64,
}

impl WaddahAttar {
    pub fn new(fast: usize, slow: usize, bb_period: usize, bb_std: f64) -> Self {
        // signal_period = 9: only .macd is used (histogram is not part of WAE).
        Self {
            macd: Macd::new(fast, slow, 9),
            bb: BBands::new(bb_period, bb_std),
            atr: Atr::new(100),
            prev_macd_line: None,
            sensitivity: 150.0,
            dz_multiplier: 3.7,
            dz_period: 100,
            fast,
            slow,
            signal_period: 9,
            bb_period,
            bb_std,
        }
    }
}

impl Strategy for WaddahAttar {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let macd_val = self.macd.update(bar.close);
        let bb_val = self.bb.update(bar.close);
        let atr_val = self.atr.update(bar.high, bar.low, bar.close);

        let (Some(m), Some(bb), Some(atr)) = (macd_val, bb_val, atr_val) else {
            return vec![];
        };

        let Some(prev_macd) = self.prev_macd_line else {
            self.prev_macd_line = Some(m.macd);
            return vec![];
        };
        self.prev_macd_line = Some(m.macd);

        // t1: change in MACD line × sensitivity — sign gives trend direction.
        let t1 = (m.macd - prev_macd) * self.sensitivity;
        let explosion_line = bb.upper - bb.lower; // BB channel width
        let deadzone_line = atr.atr * self.dz_multiplier; // ATR-based no-trade filter
        let power = t1.abs();
        let confirmed = power > explosion_line && power > deadzone_line;

        if confirmed && t1 > 0.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !confirmed || t1 <= 0.0 {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "waddah_attar"
    }

    fn reset(&mut self) {
        self.macd = Macd::new(self.fast, self.slow, self.signal_period);
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.atr = Atr::new(self.dz_period);
        self.prev_macd_line = None;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let macd1 = ind.macd(20, 40, 9);
let bb20 = ind.bbands(20, 2.0);
let atr100 = ind.atr(100);

let t1 = (macd1[0].macd - macd1[1].macd) * 150.0;
let explosion_line = bb20[0].upper - bb20[0].lower;
let deadzone_line = atr100[0].atr * 3.7;
let power = if t1 >= 0.0 { t1 } else { -t1 };
let confirmed = power > explosion_line && power > deadzone_line;

if confirmed && t1 > 0.0 { entry = true; }
if !confirmed || t1 <= 0.0 { exit = true; }
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

        let mut named = WaddahAttar::new(20, 40, 20, 2.0);
        let named_sigs = run(&mut named, &bars);

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "waddah_attar: must produce signals");
        assert_parity("waddah_attar parity vs named", &named_sigs, &script_sigs);
    }
}
