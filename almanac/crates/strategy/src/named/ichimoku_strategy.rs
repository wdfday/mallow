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
}

impl IchimokuCloud {
    pub fn new(tenkan: usize, kijun: usize, senkou_b: usize) -> Self {
        Self {
            ichi: Ichimoku::new(tenkan, kijun, senkou_b),
            tenkan,
            kijun,
            senkou_b,
        }
    }
}

impl Strategy for IchimokuCloud {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.ichi.update(bar.high, bar.low, bar.close) else {
            return vec![];
        };

        if v.above_cloud {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if !v.above_cloud {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "ichimoku_cloud"
    }

    fn reset(&mut self) {
        self.ichi = Ichimoku::new(self.tenkan, self.kijun, self.senkou_b);
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

        if bullish_cross && v.above_cloud {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if bearish_cross {
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
    }
}
