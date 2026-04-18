use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{BBands, Macd};

/// Bot #12 — Bollinger Breakthrough + MACD histogram.
///
/// Long when price breaks above upper Bollinger Band AND MACD histogram > 0.
/// Closes when price falls below middle band OR histogram turns negative.
pub struct BollingerMacd {
    bb: BBands,
    macd: Macd,
    in_position: bool,
    bb_period: usize,
    bb_std: f64,
    fast: usize,
    slow: usize,
    signal_period: usize,
}

impl BollingerMacd {
    pub fn new(bb_period: usize, bb_std: f64, fast: usize, slow: usize, signal_period: usize) -> Self {
        Self {
            bb: BBands::new(bb_period, bb_std),
            macd: Macd::new(fast, slow, signal_period),
            in_position: false,
            bb_period,
            bb_std,
            fast,
            slow,
            signal_period,
        }
    }
}

impl Strategy for BollingerMacd {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let bb_val = self.bb.update(bar.close);
        let macd_val = self.macd.update(bar.close);

        let (Some(bb), Some(m)) = (bb_val, macd_val) else {
            return vec![];
        };

        if !self.in_position && bar.close > bb.upper && m.histogram > 0.0 {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if self.in_position && (bar.close < bb.middle || m.histogram < 0.0) {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "bollinger_macd"
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.macd = Macd::new(self.fast, self.slow, self.signal_period);
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
    fn bollinger_macd_parity() {
        let bars = trending_bars(300);

        let mut hc = BollingerMacd::new(20, 2.0, 12, 26, 9);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "bb":   { "type": "bbands", "period": 20, "multiplier": 2.0 },
                "macd": { "type": "macd",   "fast": 12, "slow": 26, "signal": 9 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "gt", "compare": "bb", "compare_field": "upper" },
                { "source": "macd",  "field": "histogram", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "close", "field": "value", "op": "lt", "compare": "bb", "compare_field": "middle" },
                { "source": "macd",  "field": "histogram", "op": "lt", "value": 0.0 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > bb_upper(20) && macd_hist(12) > 0.0",
            "exit":  "close < bb_mid(20) || macd_hist(12) < 0.0"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "bollinger_macd: no signals");
        assert_parity("bollinger_macd hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("bollinger_macd hc vs cel",     &hc_sigs, &cel_sigs);
    }
}
