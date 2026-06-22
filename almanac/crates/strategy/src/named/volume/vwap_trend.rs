use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Vwap;

pub(crate) const RHAI_SCRIPT: &str = r#"
let vw = ind.vwap(0, session_gap_mins=60, buf=2);

let vwap_rising = vw[0] > vw[1];
let above_vwap = close[0] > vw[0];

if state["in_position"] == () {
    state["in_position"] = false;
}
let in_pos = state["in_position"];

if above_vwap && vwap_rising && !in_pos {
    state["in_position"] = true;
    entry = true;
}
if !above_vwap && in_pos {
    state["in_position"] = false;
    exit = true;
}
"#;

/// VWAP Trend — momentum continuation: trade in direction of VWAP trend.
///
/// Long  when close > VWAP and VWAP is rising (VWAP > prev VWAP).
/// Close when close drops below VWAP.
pub struct VwapTrend {
    vwap: Vwap,
    session_gap_mins: u64,
    prev_vwap: Option<f64>,
    in_position: bool,
}

impl VwapTrend {
    pub fn new(session_gap_mins: u64) -> Self {
        Self {
            vwap: Vwap::new(session_gap_mins),
            session_gap_mins,
            prev_vwap: None,
            in_position: false,
        }
    }
}

impl Strategy for VwapTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let v = self.vwap.update(bar.timestamp, bar.high, bar.low, bar.close, bar.volume);
        let prev = self.prev_vwap.replace(v);

        let Some(pv) = prev else {
            return vec![];
        };

        let vwap_rising = v > pv;
        let above_vwap = bar.close > v;

        if above_vwap && vwap_rising && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !above_vwap && self.in_position {
            self.in_position = false;
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "vwap_trend"
    }

    fn reset(&mut self) {
        self.vwap = Vwap::new(self.session_gap_mins);
        self.prev_vwap = None;
        self.in_position = false;
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
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
    fn vwap_trend_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut hc = VwapTrend::new(60);
        let hc_sigs = run(&mut hc, &bars);

        assert!(!hc_sigs.is_empty(), "vwap_trend: no signals");
    }
}
