use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Cci, Rsi, Stochastic};

/// Bot #18 — Oscillator Overlord.
///
/// Combines three oscillators for a consensus oversold/overbought signal.
/// Long when at least 2 of 3 are oversold; closes when at least 2 of 3 are overbought.
///
/// - RSI < 30 → oversold, RSI > 70 → overbought
/// - Stochastic %K < 20 → oversold, %K > 80 → overbought
/// - CCI < −100 → oversold, CCI > +100 → overbought
pub struct OscillatorOverlord {
    rsi: Rsi,
    stoch: Stochastic,
    cci: Cci,
    in_position: bool,
    rsi_period: usize,
    stoch_k: usize,
    stoch_d: usize,
    cci_period: usize,
}

impl OscillatorOverlord {
    pub fn new(rsi_period: usize, stoch_k: usize, stoch_d: usize, cci_period: usize) -> Self {
        Self {
            rsi: Rsi::new(rsi_period),
            stoch: Stochastic::new(stoch_k, stoch_d),
            cci: Cci::new(cci_period),
            in_position: false,
            rsi_period,
            stoch_k,
            stoch_d,
            cci_period,
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
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
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
        let bars = rsi_bars(200);

        let mut hc = OscillatorOverlord::new(14, 14, 3, 20);
        let hc_sigs = run(&mut hc, &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "(rsi(14) < 30.0 && stoch_k(14) < 20.0) || (rsi(14) < 30.0 && cci(20) < -100.0) || (stoch_k(14) < 20.0 && cci(20) < -100.0)",
            "exit":  "(rsi(14) > 70.0 && stoch_k(14) > 80.0) || (rsi(14) > 70.0 && cci(20) > 100.0) || (stoch_k(14) > 80.0 && cci(20) > 100.0)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "oscillator_overlord: no signals");
        assert_parity("oscillator_overlord hc vs cel", &hc_sigs, &cel_sigs);
    }
}
