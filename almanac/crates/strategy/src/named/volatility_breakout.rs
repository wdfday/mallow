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
}

impl VolatilityRatioBreakout {
    pub fn new(lookback: usize, threshold: f64) -> Self {
        Self {
            vr: VolatilityRatio::new(lookback),
            threshold,
            lookback,
            prev_close: None,
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

        if v > self.threshold && bullish_move {
            return vec![Signal::long(bar.timestamp, &bar.symbol, v.min(1.0))];
        }
        if v <= self.threshold {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "volatility_ratio"
    }

    fn reset(&mut self) {
        self.vr = VolatilityRatio::new(self.lookback);
        self.prev_close = None;
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
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
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
}

impl KeltnerBreakout {
    pub fn new(period: usize, atr_period: usize, multiplier: f64) -> Self {
        Self {
            keltner: Keltner::new(period, atr_period, multiplier),
            period,
            atr_period,
            multiplier,
        }
    }
}

impl Strategy for KeltnerBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.keltner.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if bar.close > v.upper {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bar.close < v.middle {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "keltner_breakout"
    }

    fn description(&self) -> &'static str {
        "Long when close breaks above upper Keltner band. Exit when close drops below the middle EMA line."
    }

    fn script(&self) -> Option<&'static str> {
        Some(r#"
let kc20 = ind.keltner(20, 1);
if close[0] > kc20[0].upper  { entry = true; }
if close[0] < kc20[0].middle { exit  = true; }
"#)
    }

    fn reset(&mut self) {
        self.keltner = Keltner::new(self.period, self.atr_period, self.multiplier);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::signal::Direction;
    use crate::test_utils::*;
    use crate::factory::build_strategy;
    use serde_json::json;

    fn run(s: &mut dyn Strategy, bars: &[Bar]) -> Vec<(i64, Direction)> {
        bars.iter().flat_map(|b| s.on_bar(b)).map(|s| (s.timestamp, s.direction)).collect()
    }

    #[test]
    fn keltner_breakout_script_parity() {
        // KeltnerBreakout fires every bar when close > upper or close < middle
        let bars = trending_bars(300);

        let mut named = KeltnerBreakout::new(20, 10, 2.0);
        let named_sigs = run(&mut named, &bars);

        let script = KeltnerBreakout::new(20, 10, 2.0).script().unwrap();
        let mut script_strat = build_strategy("script", &json!({ "script": script })).unwrap();
        let script_sigs = run(script_strat.as_mut(), &bars);

        assert!(!named_sigs.is_empty(), "keltner_breakout: must produce signals");
        assert_eq!(named_sigs, script_sigs, "script parity failed");
    }
}
