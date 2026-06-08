use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Cci, Rsi, Stochastic};

/// Bot #18 — Oscillator Overlord.
///
/// Combines three oscillators for a consensus oversold/overbought signal.
/// Long when at least 2 of 3 are oversold (and not already in position).
/// Closes when at least 2 of 3 are overbought.
///
/// - RSI < 30 → oversold, RSI > 70 → overbought
/// - Stochastic raw %K < 20 → oversold, %K > 80 → overbought
/// - CCI < −100 → oversold, CCI > +100 → overbought
pub struct OscillatorOverlord {
    rsi: Rsi,
    stoch: Stochastic,
    cci: Cci,
    rsi_period: usize,
    stoch_k: usize,
    stoch_d: usize,
    cci_period: usize,
    in_position: bool,
}

impl OscillatorOverlord {
    pub fn new(rsi_period: usize, stoch_k: usize, stoch_d: usize, cci_period: usize) -> Self {
        Self {
            rsi: Rsi::new(rsi_period),
            stoch: Stochastic::new(stoch_k, stoch_d),
            cci: Cci::new(cci_period),
            rsi_period,
            stoch_k,
            stoch_d,
            cci_period,
            in_position: false,
        }
    }
}

impl Strategy for OscillatorOverlord {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let rsi_val = self.rsi.update(bar.close);
        let stoch_val = self.stoch.update(bar.high, bar.low, bar.close);
        let cci_val = self.cci.update(bar.high, bar.low, bar.close);

        let (Some(rsi), Some(st), Some(cci)) = (rsi_val, stoch_val, cci_val) else {
            return vec![];
        };

        let os_count = [rsi < 30.0, st.k < 20.0, cci < -100.0]
            .iter()
            .filter(|&&x| x)
            .count();
        let ob_count = [rsi > 70.0, st.k > 80.0, cci > 100.0]
            .iter()
            .filter(|&&x| x)
            .count();

        if !self.in_position && os_count >= 2 {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && ob_count >= 2 {
            self.in_position = false;
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "oscillator_overlord"
    }

    fn reset(&mut self) {
        self.rsi = Rsi::new(self.rsi_period);
        self.stoch = Stochastic::new(self.stoch_k, self.stoch_d);
        self.cci = Cci::new(self.cci_period);
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    #[test]
    fn oscillator_overlord_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut hc = OscillatorOverlord::new(14, 14, 3, 20);
        let hc_sigs = run(&mut hc, &bars);

        assert!(!hc_sigs.is_empty(), "oscillator_overlord: no signals");
    }

    #[test]
    fn script_parity() {
        let Some(bars) = load_real_bars() else { return; };

        let mut named = OscillatorOverlord::new(14, 14, 3, 20);
        let named_sigs = run(&mut named, &bars);

        let script = r#"
let rsi14 = ind.rsi(14, buf=1);
let st14 = ind.stochastic(14, buf=1);
let cci20 = ind.cci(20, buf=1);
if state["in_position"] == () {
    state["in_position"] = false;
}
let os_count = 0;
if rsi14[0] < 30.0 { os_count = os_count + 1; }
if st14[0].k < 20.0 { os_count = os_count + 1; }
if cci20[0] < -100.0 { os_count = os_count + 1; }
let ob_count = 0;
if rsi14[0] > 70.0 { ob_count = ob_count + 1; }
if st14[0].k > 80.0 { ob_count = ob_count + 1; }
if cci20[0] > 100.0 { ob_count = ob_count + 1; }
if !state["in_position"] {
    if os_count >= 2 {
        state["in_position"] = true;
        entry = true;
    }
} else {
    if ob_count >= 2 {
        state["in_position"] = false;
        exit = true;
    }
}
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "oscillator_overlord: must produce signals");
        assert_parity("oscillator_overlord parity vs named", &named_sigs, &script_sigs);
    }
}
