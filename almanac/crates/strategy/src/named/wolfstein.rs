use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Adx;

/// Bot #46 — Wolfstein Trending.
///
/// Enters long when ADX > `long_threshold` AND +DI > -DI (strong bullish trend).
/// Exits when ADX falls below `short_threshold` (trend weakening).
pub struct Wolfstein {
    adx: Adx,
    long_threshold: f64,
    short_threshold: f64,
    in_position: bool,
    period: usize,
}

impl Wolfstein {
    pub fn new(period: usize, long_threshold: f64, short_threshold: f64) -> Self {
        Self {
            adx: Adx::new(period),
            long_threshold,
            short_threshold,
            in_position: false,
            period,
        }
    }
}

impl Strategy for Wolfstein {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.adx.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if !self.in_position && v.adx > self.long_threshold && v.plus_di > v.minus_di {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, v.adx / 100.0)];
        }
        if self.in_position && v.adx < self.short_threshold {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "wolfstein"
    }

    fn reset(&mut self) {
        self.adx = Adx::new(self.period);
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
    fn wolfstein_parity() {
        let bars = trending_bars(300);

        let mut hc = Wolfstein::new(14, 27.5, 20.5);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "adx": { "type": "adx", "period": 14 } },
            "entry": { "logic": "and", "rules": [
                { "source": "adx", "field": "adx",      "op": "gt", "value": 27.5 },
                { "source": "adx", "field": "plus_di",  "op": "gt",
                  "compare": "adx", "compare_field": "minus_di" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "adx", "field": "adx", "op": "lt", "value": 20.5 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "adx(14) > 27.5 && plus_di(14) > minus_di(14)",
            "exit":  "adx(14) < 20.5"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "wolfstein: no signals");
        assert_parity("wolfstein hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("wolfstein hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
