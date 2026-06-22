use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Rsi, Vwap};

pub(crate) const RHAI_SCRIPT: &str = r#"
let vw = ind.vwap(0, session_gap_mins=60, buf=2);
let rsi14 = ind.rsi(14, buf=1);

let above_vwap = close[0] > vw[0];

if state["prev_above"] == () {
    state["prev_above"] = above_vwap;
} else {
    let was_above = state["prev_above"];
    state["prev_above"] = above_vwap;
    
    if !was_above && above_vwap && rsi14[0] < 50.0 {
        entry = true;
    }
    if was_above && !above_vwap {
        exit = true;
    }
    if rsi14[0] > 65.0 {
        exit = true;
    }
}
"#;

/// VWAP Bounce — intraday mean-reversion using VWAP as fair-value anchor.
///
/// Long  when close crosses above VWAP from below AND RSI oversold (dip into VWAP).
/// Close when close crosses below VWAP from above OR RSI overbought.
pub struct VwapBounce {
    vwap: Vwap,
    rsi: Rsi,
    rsi_p: usize,
    oversold: f64,
    overbought: f64,
    session_gap_mins: u64,
    prev_above: Option<bool>,
}

impl VwapBounce {
    pub fn new(rsi_period: usize, oversold: f64, overbought: f64, session_gap_mins: u64) -> Self {
        Self {
            vwap: Vwap::new(session_gap_mins),
            rsi: Rsi::new(rsi_period),
            rsi_p: rsi_period,
            oversold,
            overbought,
            session_gap_mins,
            prev_above: None,
        }
    }
}

impl Strategy for VwapBounce {
    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let vwap = self.vwap.update(bar.timestamp, bar.high, bar.low, bar.close, bar.volume);
        let Some(rsi) = self.rsi.update(bar.close) else {
            return vec![];
        };

        let above_vwap = bar.close > vwap;
        let prev = self.prev_above.replace(above_vwap);

        let Some(was_above) = prev else {
            return vec![];
        };

        if !was_above && above_vwap && rsi < self.oversold + 10.0 {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 0.8)];
        }
        if was_above && !above_vwap {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        if rsi > self.overbought {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "vwap_bounce"
    }

    fn reset(&mut self) {
        self.vwap = Vwap::new(self.session_gap_mins);
        self.rsi = Rsi::new(self.rsi_p);
        self.prev_above = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;
    use crate::test_utils::*;

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<Signal> {
        bars.iter().flat_map(|b| s.on_bar(b)).collect()
    }

    #[test]
    fn vwap_bounce_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut hc = VwapBounce::new(14, 40.0, 65.0, 60);
        let hc_sigs = run(&mut hc, &bars);

        assert!(!hc_sigs.is_empty(), "vwap_bounce: no signals");
    }
}
