use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Sma, Stochastic};

/// Bot #30 — Range Rover.
///
/// Enters long when stochastic %K is in oversold zone AND price is above MA
/// (staying in the broader uptrend).  Exits when %K reaches overbought.
pub struct RangeRover {
    stoch: Stochastic,
    ma: Sma,
    oversold: f64,
    overbought: f64,
    in_position: bool,
    k_period: usize,
    d_period: usize,
    ma_period: usize,
}

impl RangeRover {
    pub fn new(k_period: usize, d_period: usize, ma_period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            stoch: Stochastic::new(k_period, d_period),
            ma: Sma::new(ma_period),
            oversold,
            overbought,
            in_position: false,
            k_period,
            d_period,
            ma_period,
        }
    }
}

impl Strategy for RangeRover {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let stoch_val = self.stoch.update(bar.high, bar.low, bar.close);
        let ma_val = self.ma.update(bar.close);

        let (Some(st), Some(ma)) = (stoch_val, ma_val) else {
            return vec![];
        };

        if !self.in_position && st.k < self.oversold && bar.close > ma {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && st.k > self.overbought {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "range_rover"
    }

    fn reset(&mut self) {
        self.stoch = Stochastic::new(self.k_period, self.d_period);
        self.ma = Sma::new(self.ma_period);
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
    fn range_rover_parity() {
        let bars = dip_in_uptrend_bars();

        let mut hc = RangeRover::new(14, 3, 50, 20.0, 80.0);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "stoch": { "type": "stochastic", "k_period": 14, "d_period": 3 },
                "ma":    { "type": "sma", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "stoch", "field": "k",     "op": "lt", "value": 20.0 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ma" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "stoch", "field": "k", "op": "gt", "value": 80.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "stoch_k(14) < 20.0 && close > sma(50)",
            "exit":  "stoch_k(14) > 80.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "range_rover: no signals");
        assert_parity("range_rover hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("range_rover hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
