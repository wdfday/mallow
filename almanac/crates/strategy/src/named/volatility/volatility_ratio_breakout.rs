use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::VolatilityRatio;

pub(crate) const RHAI_SCRIPT: &str = r#"
let vr = ind.volatility_ratio(10, buf=1);
if vr[0] > 0.5 && close[0] > close[1] { entry = true; }
if vr[0] <= 0.5 { exit = true; }
"#;

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

    fn description(&self) -> &'static str {
        "Long when Volatility Ratio is high and price closes higher. Exit when volatility drops."
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }

    fn reset(&mut self) {
        self.vr = VolatilityRatio::new(self.lookback);
        self.prev_close = None;
    }
}
