use alm_core::{Bar, RegimeDimension, RegimeState};

use crate::{Adx, Atr, Chop};

/// Built-in regime detector — a reference implementation using ADX, Chop, ATR, and OBV.
///
/// Produces a [`RegimeState`] with three dimensions, each carrying both a raw value
/// and a default status label. User-defined regime scripts can replace this entirely.
///
/// | Dimension  | Indicator          | Status labels                    |
/// |------------|--------------------|----------------------------------|
/// | trend      | ADX + Chop         | "trending" / "ranging" / "neutral" |
/// | volatility | ATR(short)/ATR(long) | "high" / "low" / "normal"       |
/// | liquidity  | volume / SMA(vol)  | "high" / "low" / "normal"        |
pub struct RegimeDetector {
    adx: Adx,
    chop: Chop,
    atr_short: Atr,
    atr_long: Atr,
    vol_sma: crate::Sma,
    adx_period: usize,
    chop_period: usize,
    atr_short_period: usize,
    atr_long_period: usize,
    vol_sma_period: usize,
    // Thresholds
    adx_threshold:  f64,
    chop_threshold: f64,
    vol_high_ratio: f64,
    vol_low_ratio:  f64,
    liq_high_ratio: f64,
    liq_low_ratio:  f64,
}

impl Default for RegimeDetector {
    fn default() -> Self { Self::new() }
}

impl RegimeDetector {
    pub fn new() -> Self {
        Self {
            adx:       Adx::new(14),
            chop:      Chop::new(14),
            atr_short: Atr::new(14),
            atr_long:  Atr::new(50),
            vol_sma:   crate::Sma::new(20),
            adx_period:       14,
            chop_period:      14,
            atr_short_period: 14,
            atr_long_period:  50,
            vol_sma_period:   20,
            adx_threshold:  25.0,
            chop_threshold: 61.8,
            vol_high_ratio: 1.3,
            vol_low_ratio:  0.7,
            liq_high_ratio: 1.5,
            liq_low_ratio:  0.5,
        }
    }

    pub fn with_thresholds(
        mut self,
        adx_threshold:  f64,
        chop_threshold: f64,
        vol_high_ratio: f64,
        vol_low_ratio:  f64,
    ) -> Self {
        self.adx_threshold  = adx_threshold;
        self.chop_threshold = chop_threshold;
        self.vol_high_ratio = vol_high_ratio;
        self.vol_low_ratio  = vol_low_ratio;
        self
    }

    /// Feed one bar. Returns `Some(RegimeState)` once warm.
    pub fn update(&mut self, bar: &Bar) -> Option<RegimeState> {
        let adx_val   = self.adx.update(bar.high, bar.low, bar.close);
        let chop_val  = self.chop.update(bar.high, bar.low, bar.close);
        let atr_s_val = self.atr_short.update(bar.high, bar.low, bar.close);
        let atr_l_val = self.atr_long.update(bar.high, bar.low, bar.close);
        let vol_ma    = self.vol_sma.update(bar.volume);

        let adx  = adx_val?.adx;
        let chop = chop_val?;

        // ── Trend ─────────────────────────────────────────────────────────────
        let (trend_status, trend_value) = if adx > self.adx_threshold {
            ("trending", adx)
        } else if chop > self.chop_threshold {
            ("ranging", chop)
        } else {
            ("neutral", adx)
        };

        // ── Volatility ────────────────────────────────────────────────────────
        let (vol_status, vol_value) = match (atr_s_val, atr_l_val) {
            (Some(s), Some(l)) if l.atr > f64::EPSILON => {
                let ratio = s.atr / l.atr;
                let status = if ratio > self.vol_high_ratio { "high" }
                             else if ratio < self.vol_low_ratio { "low" }
                             else { "normal" };
                (status, ratio)
            }
            _ => ("normal", 1.0),
        };

        // ── Liquidity ─────────────────────────────────────────────────────────
        let (liq_status, liq_value) = match vol_ma {
            Some(ma) if ma > f64::EPSILON => {
                let ratio = bar.volume / ma;
                let status = if ratio > self.liq_high_ratio { "high" }
                             else if ratio < self.liq_low_ratio { "low" }
                             else { "normal" };
                (status, ratio)
            }
            _ => ("normal", 1.0),
        };

        Some(RegimeState::new(
            RegimeDimension::new(trend_value, trend_status),
            RegimeDimension::new(vol_value,   vol_status),
            RegimeDimension::new(liq_value,   liq_status),
        ))
    }

    pub fn reset(&mut self) {
        self.adx       = Adx::new(self.adx_period);
        self.chop      = Chop::new(self.chop_period);
        self.atr_short = Atr::new(self.atr_short_period);
        self.atr_long  = Atr::new(self.atr_long_period);
        self.vol_sma   = crate::Sma::new(self.vol_sma_period);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::Bar;

    fn bar(ts: i64, o: f64, h: f64, l: f64, c: f64) -> Bar {
        Bar::new(ts, "TEST", o, h, l, c, 1000.0)
    }

    #[test]
    fn no_regime_before_warmup() {
        let mut det = RegimeDetector::new();
        for i in 0..13 {
            assert!(det.update(&bar(i, 100.0, 101.0, 99.0, 100.0)).is_none());
        }
    }

    #[test]
    fn trending_regime_high_adx() {
        let mut det = RegimeDetector::new();
        for i in 0..60 {
            let base = 100.0 + i as f64;
            det.update(&bar(i, base, base + 2.0, base - 0.5, base + 1.5));
        }
        let regime = det.update(&bar(60, 160.0, 162.0, 159.5, 161.5)).unwrap();
        assert!(regime.trend.is("trending"));
    }

    #[test]
    fn ranging_regime_choppy() {
        let mut det = RegimeDetector::new();
        for i in 0..60 {
            let (h, l, c) = if i % 2 == 0 { (102.0, 98.0, 101.0) } else { (102.0, 98.0, 99.0) };
            det.update(&bar(i, 100.0, h, l, c));
        }
        let regime = det.update(&bar(60, 100.0, 102.0, 98.0, 100.0)).unwrap();
        assert!(!regime.trend.is("trending"));
    }

    #[test]
    fn reset_clears_state() {
        let mut det = RegimeDetector::new();
        for i in 0..30 {
            let base = 100.0 + i as f64;
            det.update(&bar(i, base, base + 1.0, base - 1.0, base));
        }
        det.reset();
        assert!(det.update(&bar(0, 100.0, 101.0, 99.0, 100.0)).is_none());
    }
}
