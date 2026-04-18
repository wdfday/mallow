use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Ichimoku;

/// Bot — Ichimoku Cloud.
///
/// Long when price is above the cloud (Senkou A and Senkou B).
/// Close when price drops back below the cloud.
pub struct IchimokuCloud {
    ichi: Ichimoku,
    tenkan: usize,
    kijun: usize,
    senkou_b: usize,
    in_position: bool,
}

impl IchimokuCloud {
    pub fn new(tenkan: usize, kijun: usize, senkou_b: usize) -> Self {
        Self {
            ichi: Ichimoku::new(tenkan, kijun, senkou_b),
            tenkan,
            kijun,
            senkou_b,
            in_position: false,
        }
    }
}

impl Strategy for IchimokuCloud {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.ichi.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if v.above_cloud && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.above_cloud && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "ichimoku_cloud"
    }

    fn reset(&mut self) {
        self.ichi = Ichimoku::new(self.tenkan, self.kijun, self.senkou_b);
        self.in_position = false;
    }
}

/// Bot — Ichimoku TK Cross.
///
/// Long when Tenkan-sen crosses above Kijun-sen AND price is above cloud.
/// Close when Tenkan crosses below Kijun.
pub struct IchimokuCross {
    ichi: Ichimoku,
    tenkan: usize,
    kijun: usize,
    senkou_b: usize,
    prev_tenkan: Option<f64>,
    prev_kijun: Option<f64>,
    in_position: bool,
}

impl IchimokuCross {
    pub fn new(tenkan: usize, kijun: usize, senkou_b: usize) -> Self {
        Self {
            ichi: Ichimoku::new(tenkan, kijun, senkou_b),
            tenkan,
            kijun,
            senkou_b,
            prev_tenkan: None,
            prev_kijun: None,
            in_position: false,
        }
    }
}

impl Strategy for IchimokuCross {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.ichi.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        let (Some(pt), Some(pk)) = (self.prev_tenkan, self.prev_kijun) else {
            self.prev_tenkan = Some(v.tenkan);
            self.prev_kijun = Some(v.kijun);
            return vec![];
        };

        let bullish_cross = pt <= pk && v.tenkan > v.kijun;
        let bearish_cross = pt >= pk && v.tenkan < v.kijun;

        self.prev_tenkan = Some(v.tenkan);
        self.prev_kijun = Some(v.kijun);

        if bullish_cross && v.above_cloud && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bearish_cross && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "ichimoku_cross"
    }

    fn reset(&mut self) {
        self.ichi = Ichimoku::new(self.tenkan, self.kijun, self.senkou_b);
        self.prev_tenkan = None;
        self.prev_kijun = None;
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
    fn ichimoku_cloud_parity() {
        let bars = trending_bars(400);

        let mut hc = IchimokuCloud::new(9, 26, 52);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "ichi": { "type": "ichimoku", "tenkan": 9, "kijun": 26, "senkou_b": 52 } },
            "entry": { "logic": "and", "rules": [
                { "source": "ichi", "field": "above_cloud", "op": "gt", "value": 0.5 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "ichi", "field": "above_cloud", "op": "lt", "value": 0.5 }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "ichimoku_cloud: no signals");
        assert_parity("ichimoku_cloud hc vs dynamic", &hc_sigs, &dyn_sigs);
    }

    #[test]
    fn ichimoku_cross_parity() {
        let bars = trending_bars(600);

        let mut hc = IchimokuCross::new(9, 26, 52);
        let hc_sigs = run(&mut hc, &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "ichi": { "type": "ichimoku", "tenkan": 9, "kijun": 26, "senkou_b": 52 } },
            "entry": { "logic": "and", "rules": [
                { "source": "ichi", "field": "tenkan", "op": "cross_above",
                  "compare": "ichi", "compare_field": "kijun" },
                { "source": "ichi", "field": "above_cloud", "op": "gt", "value": 0.5 }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "ichi", "field": "tenkan", "op": "cross_below",
                  "compare": "ichi", "compare_field": "kijun" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "ichimoku_cross: no signals");
        assert_parity("ichimoku_cross hc vs dynamic", &hc_sigs, &dyn_sigs);
    }
}
