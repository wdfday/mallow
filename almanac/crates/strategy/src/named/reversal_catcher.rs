use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Rsi, Stochastic};

/// Bot #31 — Reversal Catcher.
///
/// Long when %K crosses above %D (momentum turning up) AND RSI < 50.
/// Closes when %K crosses back below %D OR RSI > 70.
pub struct ReversalCatcher {
    stoch: Stochastic,
    rsi: Rsi,
    prev_k: Option<f64>,
    prev_d: Option<f64>,
    in_position: bool,
    k_period: usize,
    d_period: usize,
    rsi_period: usize,
}

impl ReversalCatcher {
    pub fn new(k_period: usize, d_period: usize, rsi_period: usize) -> Self {
        Self {
            stoch: Stochastic::new(k_period, d_period),
            rsi: Rsi::new(rsi_period),
            prev_k: None,
            prev_d: None,
            in_position: false,
            k_period,
            d_period,
            rsi_period,
        }
    }
}

impl Strategy for ReversalCatcher {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let stoch_val = self.stoch.update(bar.high, bar.low, bar.close);
        let rsi_val = self.rsi.update(bar.close);

        let (Some(st), Some(rsi)) = (stoch_val, rsi_val) else {
            return vec![];
        };

        let (Some(pk), Some(pd)) = (self.prev_k, self.prev_d) else {
            self.prev_k = Some(st.k);
            self.prev_d = Some(st.d);
            return vec![];
        };

        let k_crossed_above_d = pk <= pd && st.k > st.d;
        let k_crossed_below_d = pk >= pd && st.k < st.d;

        self.prev_k = Some(st.k);
        self.prev_d = Some(st.d);

        if k_crossed_above_d && rsi < 50.0 && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (k_crossed_below_d || rsi > 70.0) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "reversal_catcher"
    }

    fn reset(&mut self) {
        self.stoch = Stochastic::new(self.k_period, self.d_period);
        self.rsi = Rsi::new(self.rsi_period);
        self.prev_k = None;
        self.prev_d = None;
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
    fn reversal_catcher_parity() {
        let bars = rsi_bars(200);

        let mut hc = ReversalCatcher::new(14, 3, 14);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "stoch": { "type": "stochastic", "k_period": 14, "d_period": 3 },
                "rsi":   { "type": "rsi", "period": 14 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "stoch", "field": "k", "op": "cross_above",
                  "compare": "stoch", "compare_field": "d" },
                { "source": "rsi", "field": "value", "op": "lt", "value": 50.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "stoch", "field": "k", "op": "cross_below",
                  "compare": "stoch", "compare_field": "d" },
                { "source": "rsi", "field": "value", "op": "gt", "value": 70.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_stoch_k(14) <= prev_stoch_d(14) && stoch_k(14) > stoch_d(14) && rsi(14) < 50.0",
            "exit":  "(prev_stoch_k(14) >= prev_stoch_d(14) && stoch_k(14) < stoch_d(14)) || rsi(14) > 70.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "reversal_catcher: no signals");
        assert_parity("reversal_catcher hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("reversal_catcher hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
