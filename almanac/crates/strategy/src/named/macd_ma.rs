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
            prev_hist: None,
            in_position: false,
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

        if hist_crossed_up && bar.close > ma && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (hist_crossed_down || bar.close < ma) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
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
    fn macd_ma_parity() {
        let bars = dip_in_uptrend_bars();

        let mut hc = MacdMa::new(12, 26, 9, 50);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "macd": { "type": "macd", "fast": 12, "slow": 26, "signal": 9 },
                "ma":   { "type": "sma",  "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "macd", "field": "histogram", "op": "cross_above", "value": 0.0 },
                { "source": "close", "field": "value", "op": "gt", "compare": "ma" }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "macd", "field": "histogram", "op": "cross_below", "value": 0.0 },
                { "source": "close", "field": "value", "op": "lt", "compare": "ma" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_macd_hist(12) <= 0.0 && macd_hist(12) > 0.0 && close > sma(50)",
            "exit":  "(prev_macd_hist(12) >= 0.0 && macd_hist(12) < 0.0) || close < sma(50)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "macd_ma: no signals");
        assert_parity("macd_ma hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("macd_ma hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
