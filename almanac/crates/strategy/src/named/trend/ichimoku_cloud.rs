use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Ichimoku;

pub(crate) const RHAI_SCRIPT: &str = r#"
let ic = ind.ichimoku(9, buf=1);
if ic[0].above_cloud >= 0.5 { entry = true; }
if ic[0].below_cloud >= 0.5 { exit  = true; }
"#;

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
        if v.below_cloud {
            return vec![Signal::exit(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "ichimoku_cloud"
    }

    fn reset(&mut self) {
        self.ichi = Ichimoku::new(self.tenkan, self.kijun, self.senkou_b);
    }

    fn script(&self) -> Option<&'static str> {
        Some(RHAI_SCRIPT)
    }
}
