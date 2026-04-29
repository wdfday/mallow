use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Ema;

/// Bot #35 — Triple EMA Crossover.
///
/// Long when all three EMAs are aligned bullishly: EMA(fast) > EMA(mid) > EMA(slow).
/// Entry triggered when the fast EMA crosses above the mid EMA (with slow already below mid).
/// Closes when fast EMA crosses back below mid EMA.
pub struct TripleEma {
    ema1: Ema,
    ema2: Ema,
    ema3: Ema,
    prev_e1: Option<f64>,
    prev_e2: Option<f64>,
    in_position: bool,
    period1: usize,
    period2: usize,
    period3: usize,
}

impl TripleEma {
    pub fn new(period1: usize, period2: usize, period3: usize) -> Self {
        assert!(period1 < period2 && period2 < period3, "periods must be ascending");
        Self {
            ema1: Ema::new(period1),
            ema2: Ema::new(period2),
            ema3: Ema::new(period3),
            prev_e1: None,
            prev_e2: None,
            in_position: false,
            period1,
            period2,
            period3,
        }
    }
}

impl Strategy for TripleEma {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let e1 = self.ema1.update(bar.close);
        let e2 = self.ema2.update(bar.close);
        let e3 = self.ema3.update(bar.close);

        let (Some(e1), Some(e2), Some(e3)) = (e1, e2, e3) else {
            return vec![];
        };

        let (Some(pe1), Some(pe2)) = (self.prev_e1, self.prev_e2) else {
            self.prev_e1 = Some(e1);
            self.prev_e2 = Some(e2);
            return vec![];
        };

        let fast_crossed_above_mid = pe1 <= pe2 && e1 > e2;
        let fast_crossed_below_mid = pe1 >= pe2 && e1 < e2;

        self.prev_e1 = Some(e1);
        self.prev_e2 = Some(e2);

        // Bullish: fast crosses mid AND mid already above slow
        if fast_crossed_above_mid && e2 > e3 && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if fast_crossed_below_mid && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "triple_ema"
    }

    fn reset(&mut self) {
        self.ema1 = Ema::new(self.period1);
        self.ema2 = Ema::new(self.period2);
        self.ema3 = Ema::new(self.period3);
        self.prev_e1 = None;
        self.prev_e2 = None;
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
    fn triple_ema_parity() {
        let bars = dip_in_uptrend_bars();

        let mut hc = TripleEma::new(10, 20, 50);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": {
                "e1": { "type": "ema", "period": 10 },
                "e2": { "type": "ema", "period": 20 },
                "e3": { "type": "ema", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "e1", "field": "value", "op": "cross_above",
                  "compare": "e2", "compare_field": "value" },
                { "source": "e2", "field": "value", "op": "gt",
                  "compare": "e3", "compare_field": "value" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "e1", "field": "value", "op": "cross_below",
                  "compare": "e2", "compare_field": "value" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "prev_ema(10) <= prev_ema(20) && ema(10) > ema(20) && ema(20) > ema(50)",
            "exit":  "prev_ema(10) >= prev_ema(20) && ema(10) < ema(20)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "triple_ema: no signals");
        assert_parity("triple_ema hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("triple_ema hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
