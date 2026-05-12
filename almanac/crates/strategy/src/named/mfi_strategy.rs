use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_indicator::Mfi;

/// Bot — MFI Trend.
///
/// Long when MFI crosses above `bull_threshold` (default 50) — money is flowing in.
/// Close when MFI drops below `bear_threshold` (default 40).
pub struct MfiTrend {
    mfi: Mfi,
    bull_threshold: f64,
    bear_threshold: f64,
    prev_mfi: Option<f64>,
    period: usize,
}

impl MfiTrend {
    pub fn new(period: usize, bull_threshold: f64, bear_threshold: f64) -> Self {
        Self {
            mfi: Mfi::new(period),
            bull_threshold,
            bear_threshold,
            prev_mfi: None,
            period,
        }
    }
}

impl Strategy for MfiTrend {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.mfi.update(bar.high, bar.low, bar.close, bar.volume) else {
            return vec![];
        };

        let prev = self.prev_mfi.replace(v);
        let Some(p) = prev else {
            return vec![];
        };

        if p <= self.bull_threshold && v > self.bull_threshold {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if v < self.bear_threshold {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "mfi_trend"
    }

    fn reset(&mut self) {
        self.mfi = Mfi::new(self.period);
        self.prev_mfi = None;
    }
}

/// Bot — MFI Mean Reversion.
///
/// Long when MFI crosses above `oversold` (default 20) — oversold recovery.
/// Close when MFI crosses above `overbought` (default 80).
pub struct MfiRevert {
    mfi: Mfi,
    oversold: f64,
    overbought: f64,
    prev_mfi: Option<f64>,
    period: usize,
}

impl MfiRevert {
    pub fn new(period: usize, oversold: f64, overbought: f64) -> Self {
        Self {
            mfi: Mfi::new(period),
            oversold,
            overbought,
            prev_mfi: None,
            period,
        }
    }
}

impl Strategy for MfiRevert {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let Some(v) = self.mfi.update(bar.high, bar.low, bar.close, bar.volume) else {
            return vec![];
        };

        let prev = self.prev_mfi.replace(v);
        let Some(p) = prev else {
            return vec![];
        };

        if p <= self.oversold && v > self.oversold  {
            return vec![Signal::long(bar.timestamp, &bar.symbol, 1.0)];
        }
        if p <= self.overbought && v > self.overbought  {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }
        vec![]
    }

    fn name(&self) -> &str {
        "mfi_revert"
    }

    fn reset(&mut self) {
        self.mfi = Mfi::new(self.period);
        self.prev_mfi = None;
    }
}
