use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::{Cmf, Ema};

/// Chaikin Money Flow + EMA trend filter.
///
/// Long when CMF > bull_threshold (strong buying pressure) and close > EMA.
/// Close when CMF < -bear_threshold (selling pressure) or close < EMA.
pub struct CmfEmaTrend {
    cmf: Cmf,
    ema: Ema,
    cmf_period: usize,
    ema_period: usize,
    bull_threshold: f64,
    bear_threshold: f64,
}

impl CmfEmaTrend {
    pub fn new(
        cmf_period: usize,
        ema_period: usize,
        bull_threshold: f64,
        bear_threshold: f64,
    ) -> Self {
        Self {
            cmf: Cmf::new(cmf_period),
            ema: Ema::new(ema_period),
            cmf_period,
            ema_period,
            bull_threshold,
            bear_threshold,
        }
    }
}

impl Strategy for CmfEmaTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let cmf = self.cmf.update(bar.high, bar.low, bar.close, bar.volume);
        let ema = self.ema.update(bar.close);

        let (Some(cmf), Some(ema)) = (cmf, ema) else {
            return vec![];
        };

        if cmf > self.bull_threshold && bar.close > ema {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if cmf < -self.bear_threshold || bar.close < ema {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "cmf_ema_trend"
    }

    fn reset(&mut self) {
        self.cmf = Cmf::new(self.cmf_period);
        self.ema = Ema::new(self.ema_period);
    }
}
