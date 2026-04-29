use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{BBands, Keltner, VolatilityRatio};

/// Bot — Volatility Ratio Breakout (Schwager).
///
/// Long when VR > `threshold` (explosive directional move) AND close > previous close.
/// Close when VR drops back below `threshold`.
pub struct VolatilityRatioBreakout {
    vr: VolatilityRatio,
    threshold: f64,
    lookback: usize,
    prev_close: Option<f64>,
    in_position: bool,
}

impl VolatilityRatioBreakout {
    pub fn new(lookback: usize, threshold: f64) -> Self {
        Self {
            vr: VolatilityRatio::new(lookback),
            threshold,
            lookback,
            prev_close: None,
            in_position: false,
        }
    }
}

impl Strategy for VolatilityRatioBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.vr.update(bar.high, bar.low, bar.close) else {
            self.prev_close = Some(bar.close);
            return vec![];
        };

        let prev_close = self.prev_close.replace(bar.close);

        let bullish_move = prev_close.map_or(false, |pc| bar.close > pc);

        if v > self.threshold && bullish_move && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, v.min(1.0))];
        }
        if v <= self.threshold && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "volatility_ratio"
    }

    fn reset(&mut self) {
        self.vr = VolatilityRatio::new(self.lookback);
        self.prev_close = None;
        self.in_position = false;
    }
}

/// Bot — BB / Keltner Squeeze.
///
/// Detects a squeeze when Bollinger Bands are inside Keltner Channel.
/// Enters long when squeeze releases with price moving up (close > BB middle).
/// Closes when close drops below BB middle.
pub struct BbKeltnerSqueeze {
    bb: BBands,
    keltner: Keltner,
    bb_period: usize,
    bb_std: f64,
    keltner_period: usize,
    keltner_atr: usize,
    keltner_mult: f64,
    was_squeezed: bool,
    in_position: bool,
}

impl BbKeltnerSqueeze {
    pub fn new(
        bb_period: usize,
        bb_std: f64,
        keltner_period: usize,
        keltner_atr: usize,
        keltner_mult: f64,
    ) -> Self {
        Self {
            bb: BBands::new(bb_period, bb_std),
            keltner: Keltner::new(keltner_period, keltner_atr, keltner_mult),
            bb_period,
            bb_std,
            keltner_period,
            keltner_atr,
            keltner_mult,
            was_squeezed: false,
            in_position: false,
        }
    }
}

impl Strategy for BbKeltnerSqueeze {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let bb = self.bb.update(bar.close);
        let kc = self.keltner.update(bar.high, bar.low, bar.close);

        let (Some(bb), Some(kc)) = (bb, kc) else {
            return vec![];
        };

        // Squeeze: BB bands inside Keltner
        let squeezed = bb.upper < kc.upper && bb.lower > kc.lower;

        let squeeze_released = self.was_squeezed && !squeezed;
        self.was_squeezed = squeezed;

        // On release with upward momentum
        if squeeze_released && bar.close > bb.middle && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < bb.middle && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "bb_keltner_squeeze"
    }

    fn reset(&mut self) {
        self.bb = BBands::new(self.bb_period, self.bb_std);
        self.keltner = Keltner::new(self.keltner_period, self.keltner_atr, self.keltner_mult);
        self.was_squeezed = false;
        self.in_position = false;
    }
}

/// Bot — Keltner Channel Breakout.
///
/// Long when close breaks above the upper Keltner band.
/// Close when close drops below the middle (EMA) line.
pub struct KeltnerBreakout {
    keltner: Keltner,
    period: usize,
    atr_period: usize,
    multiplier: f64,
    in_position: bool,
}

impl KeltnerBreakout {
    pub fn new(period: usize, atr_period: usize, multiplier: f64) -> Self {
        Self {
            keltner: Keltner::new(period, atr_period, multiplier),
            period,
            atr_period,
            multiplier,
            in_position: false,
        }
    }
}

impl Strategy for KeltnerBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.keltner.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if bar.close > v.upper && !self.in_position {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < v.middle && self.in_position {
            self.in_position = false;
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "keltner_breakout"
    }

    fn reset(&mut self) {
        self.keltner = Keltner::new(self.period, self.atr_period, self.multiplier);
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
    fn keltner_breakout_parity() {
        let bars = trending_bars(300);

        let mut hc = build_strategy("keltner_breakout", &json!({})).unwrap();
        let hc_sigs = run(hc.as_mut(), &bars);

        let mut dyn_s = build_strategy("dynamic", &json!({
            "indicators": { "kc": { "type": "keltner", "period": 20, "atr_period": 10, "multiplier": 2.0 } },
            "entry": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "gt",
                  "compare": "kc", "compare_field": "upper" }
            ]},
            "exit": { "logic": "and", "rules": [
                { "source": "close", "field": "value", "op": "lt",
                  "compare": "kc", "compare_field": "middle" }
            ]}
        })).unwrap();
        let dyn_sigs = run(dyn_s.as_mut(), &bars);

        let mut cel = build_strategy("cel", &json!({
            "entry": "close > keltner_upper(20)",
            "exit":  "close < keltner_mid(20)"
        })).unwrap();
        let cel_sigs = run(cel.as_mut(), &bars);

        assert!(!hc_sigs.is_empty(), "keltner_breakout: no signals");
        assert_parity("keltner_breakout hc vs dynamic", &hc_sigs, &dyn_sigs);
        assert_parity("keltner_breakout hc vs cel",     &hc_sigs, &cel_sigs);
    }
    */
}
