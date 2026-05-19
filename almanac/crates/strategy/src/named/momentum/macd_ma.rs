use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Macd, Sma};

/// Bot #3 — MACD with MA filter.
///
/// Long when MACD histogram crosses above zero AND price is above MA.
/// Closes when histogram crosses below zero OR price drops below MA.
pub struct MacdMa {
    macd: Macd,
    ma: Sma,
    prev_hist: Option<f64>,
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
            prev_hist: None,
            fast,
            slow,
            signal_period,
            ma_period,
        }
    }
}

impl Strategy for MacdMa {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let macd_val = self.macd.update(bar.close);
        let ma_val = self.ma.update(bar.close);

        let (Some(m), Some(ma)) = (macd_val, ma_val) else {
            return vec![];
        };

        let Some(prev) = self.prev_hist else {
            self.prev_hist = Some(m.histogram);
            return vec![];
        };

        let hist_crossed_up = prev <= 0.0 && m.histogram > 0.0;
        let hist_crossed_down = prev >= 0.0 && m.histogram < 0.0;
        self.prev_hist = Some(m.histogram);

        if hist_crossed_up && bar.close > ma {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if hist_crossed_down || bar.close < ma {
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
        self.prev_hist = None;
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
        let bars = slow_trend_bars();

        let mut named = MacdMa::new(12, 26, 9, 50);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let mh = ind.macd(12);
let sma50 = ind.sma(50, 1);
if mh[1].histogram <= 0.0 && mh[0].histogram > 0.0 && close[0] > sma50[0] { entry = true; }
if (mh[1].histogram >= 0.0 && mh[0].histogram < 0.0) || close[0] < sma50[0] { exit = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

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
    
    let slow = slow_trend_bars();
    let mut ma = MacdMa::new(12, 26, 9, 50);
    let s: Vec<_> = slow.iter().flat_map(|b| ma.on_bar(b)).collect();
    eprintln!("macd_ma slow_trend_bars: {} / {}", s.len(), slow.len());
    
    let trend400 = trending_bars(400);
    let mut te = TripleEma::new(10, 20, 50);
    let s: Vec<_> = trend400.iter().flat_map(|b| te.on_bar(b)).collect();
    eprintln!("triple_ema trending(400): {}", s.len());
    
    let dip = dip_in_uptrend_bars();
    let mut te2 = TripleEma::new(10, 20, 50);
    let s: Vec<_> = dip.iter().flat_map(|b| te2.on_bar(b)).collect();
    eprintln!("triple_ema dip: {}", s.len());
    
    let trend300 = trending_bars(300);
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
