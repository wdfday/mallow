use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Macd, Sma};

/// Bot #3 — MACD with MA filter.
///
/// Long when MACD histogram > 0 AND price is above EMA (state-based, not crossover).
/// Closes when histogram < 0.
pub struct MacdMa {
    macd: Macd,
    ma: Sma,
    in_position: bool,
    fast: usize,
    slow: usize,
    signal_period: usize,
    ma_period: usize,
}

impl MacdMa {
    pub fn new(fast: usize, slow: usize, signal_period: usize, ma_period: usize) -> Self {
        Self {
            macd: Macd::new(fast, slow, signal_period),
            ma: Sma::new(ma_period),
            in_position: false,
            fast,
            slow,
            signal_period,
            ma_period,
        }
    }
}

impl Strategy for MacdMa {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let macd_val = self.macd.update(bar.close);
        let ma_val = self.ma.update(bar.close);

        let (Some(m), Some(ma)) = (macd_val, ma_val) else {
            return vec![];
        };

        if !self.in_position && m.histogram > 0.0 && bar.close > ma {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && m.histogram < 0.0 {
            self.in_position = false;
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "macd_ma"
    }

    fn reset(&mut self) {
        self.macd = Macd::new(self.fast, self.slow, self.signal_period);
        self.ma = Sma::new(self.ma_period);
        self.in_position = false;
    }
}


pub(crate) const RHAI_SCRIPT: &str = r#"
let mh = ind.macd(12, buf=1);
let sma50 = ind.sma(50, buf=1);
let in_pos = state["in_position"] == true;
if !in_pos && mh[0].histogram > 0.0 && close[0] > sma50[0] { entry = true; state["in_position"] = true; }
if in_pos && mh[0].histogram < 0.0 { exit = true; state["in_position"] = false; }
"#;
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

        let mut named = MacdMa::new(12, 26, 9, 50);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = RHAI_SCRIPT;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "macd_ma: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}

#[cfg(test)]
#[test]
fn signal_count_debug() {
    use crate::named::triple_ema::TripleEma;
    use crate::named::cmo_zero_cross::CmoZeroCross;
    use crate::test_utils::*;
    use alm_core::strategy::Strategy;
    
    let Some(slow) = load_real_bars() else { return; };
    let mut ma = MacdMa::new(12, 26, 9, 50);
    let s: Vec<_> = slow.iter().flat_map(|b| ma.on_bar(b)).collect();
    eprintln!("macd_ma slow_trend_bars: {} / {}", s.len(), slow.len());
    
    let Some(trend400) = load_real_bars() else { return; };
    let mut te = TripleEma::new(10, 20, 50);
    let s: Vec<_> = trend400.iter().flat_map(|b| te.on_bar(b)).collect();
    eprintln!("triple_ema trending(400): {}", s.len());
    
    let Some(dip) = load_real_bars() else { return; };
    let mut te2 = TripleEma::new(10, 20, 50);
    let s: Vec<_> = dip.iter().flat_map(|b| te2.on_bar(b)).collect();
    eprintln!("triple_ema dip: {}", s.len());
    
    let Some(trend300) = load_real_bars() else { return; };
    let mut c = CmoZeroCross::new(14, 50);
    let s: Vec<_> = trend300.iter().flat_map(|b| c.on_bar(b)).collect();
    eprintln!("cmo trending(300): {}", s.len());
    
    let mut c2 = CmoZeroCross::new(14, 50);
    let s: Vec<_> = slow.iter().flat_map(|b| c2.on_bar(b)).collect();
    eprintln!("cmo slow_trend: {}", s.len());
    
    let mut c3 = CmoZeroCross::new(14, 50);
    let s: Vec<_> = dip.iter().flat_map(|b| c3.on_bar(b)).collect();
    eprintln!("cmo dip: {}", s.len());
}
