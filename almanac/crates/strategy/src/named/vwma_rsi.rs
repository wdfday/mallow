use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Rsi, Vwma};

/// Volume-Weighted MA + RSI momentum filter.
///
/// VWMA above SMA signals institutional accumulation. RSI > 50 confirms momentum.
///
/// Long when price > VWMA AND RSI > rsi_entry.
/// Close when price < VWMA OR RSI < rsi_exit.
pub struct VwmaRsi {
    vwma: Vwma,
    rsi: Rsi,
    vwma_period: usize,
    rsi_period: usize,
    rsi_entry: f64,
    rsi_exit: f64,
}

impl VwmaRsi {
    pub fn new(vwma_period: usize, rsi_period: usize, rsi_entry: f64, rsi_exit: f64) -> Self {
        Self {
            vwma: Vwma::new(vwma_period),
            rsi: Rsi::new(rsi_period),
            vwma_period,
            rsi_period,
            rsi_entry,
            rsi_exit,
        }
    }
}

impl Strategy for VwmaRsi {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let vwma = self.vwma.update(bar.close, bar.volume);
        let rsi = self.rsi.update(bar.close);

        let (Some(vwma), Some(rsi)) = (vwma, rsi) else {
            return vec![];
        };

        if bar.close > vwma && rsi > self.rsi_entry {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < vwma || rsi < self.rsi_exit {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "vwma_rsi"
    }

    fn reset(&mut self) {
        self.vwma = Vwma::new(self.vwma_period);
        self.rsi = Rsi::new(self.rsi_period);
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
        let bars = trending_bars(300);

        let mut named = VwmaRsi::new(20, 14, 50.0, 45.0);
        let named_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| named.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        let script = r#"
let vwma20 = ind.vwma(20, 1);
let rsi14 = ind.rsi(14, 1);
if close[0] > vwma20[0] && rsi14[0] > 50.0 { entry = true; }
if close[0] < vwma20[0] || rsi14[0] < 45.0 { exit = true; }
"#;
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs: Vec<(i64, Direction)> = bars.iter()
            .flat_map(|b| script_strat.on_bar(b))
            .map(|s| (s.timestamp, s.direction))
            .collect();

        assert!(!named_sigs.is_empty(), "vwma_rsi: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
