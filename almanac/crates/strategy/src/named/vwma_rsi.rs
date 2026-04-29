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
    in_position: bool,
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
            in_position: false,
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

        if bar.close > vwma && rsi > self.rsi_entry && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (bar.close < vwma || rsi < self.rsi_exit) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "vwma_rsi"
    }

    fn reset(&mut self) {
        self.vwma = Vwma::new(self.vwma_period);
        self.rsi = Rsi::new(self.rsi_period);
        self.in_position = false;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    /* // deprecated — DynamicStrategy removed
    #[test]
    fn vwma_rsi_parity() {
        let bars = trending_bars(300);

        let mut hc = VwmaRsi::new(20, 14, 50.0, 45.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "vwma": { "type": "vwma", "period": 20 },
                "rsi":  { "type": "rsi",  "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "gt", "compare": "vwma" },
                { "source": "rsi",   "field": "value", "op": "gt", "value": 50.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "close", "field": "value", "op": "lt", "compare": "vwma" },
                { "source": "rsi",   "field": "value", "op": "lt", "value": 45.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > vwma(20) && rsi(14) > 50.0",
            "exit":  "close < vwma(20) || rsi(14) < 45.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "vwma_rsi: no signals");
        assert_parity("vwma_rsi hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("vwma_rsi hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
