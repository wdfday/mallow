use alm_core::{Bar, RegimeState, TrendRegime, VolRegime};

use crate::{Adx, Atr, Chop};

/// Detects the current market regime using ADX, Chop, and ATR ratio.
///
/// Two independent dimensions:
/// - **Trend**: Trending (ADX > threshold) | Ranging (Chop > threshold) | Neutral
/// - **Volatility**: High (ATR_short/ATR_long > ratio) | Low | Normal
///
/// # Example
/// ```rust,ignore
/// let mut detector = RegimeDetector::new();
/// for bar in bars {
///     if let Some(regime) = detector.update(&bar) {
///         println!("{}", regime.label()); // e.g. "Trending/High"
///     }
/// }
/// ```
pub struct RegimeDetector {
    adx: Adx,
    chop: Chop,
    atr_short: Atr, // current volatility (default period 14)
    atr_long: Atr,  // baseline volatility (default period 50)
    // Periods (kept so reset() can rebuild with the same configuration)
    adx_period: usize,
    chop_period: usize,
    atr_short_period: usize,
    atr_long_period: usize,
    // Thresholds
    adx_threshold: f64,  // default 25.0
    chop_threshold: f64, // default 61.8 (Fibonacci golden ratio)
    vol_high_ratio: f64, // default 1.3
    vol_low_ratio: f64,  // default 0.7
}

impl Default for RegimeDetector {
    fn default() -> Self {
        Self::new()
    }
}

impl RegimeDetector {
    /// Create with default periods and thresholds.
    pub fn new() -> Self {
        Self {
            adx: Adx::new(14),
            chop: Chop::new(14),
            atr_short: Atr::new(14),
            atr_long: Atr::new(50),
            adx_period: 14,
            chop_period: 14,
            atr_short_period: 14,
            atr_long_period: 50,
            adx_threshold: 25.0,
            chop_threshold: 61.8,
            vol_high_ratio: 1.3,
            vol_low_ratio: 0.7,
        }
    }

    /// Customise indicator lookback periods.
    pub fn with_periods(
        mut self,
        adx_period: usize,
        chop_period: usize,
        atr_short_period: usize,
        atr_long_period: usize,
    ) -> Self {
        self.adx = Adx::new(adx_period);
        self.chop = Chop::new(chop_period);
        self.atr_short = Atr::new(atr_short_period);
        self.atr_long = Atr::new(atr_long_period);
        self.adx_period = adx_period;
        self.chop_period = chop_period;
        self.atr_short_period = atr_short_period;
        self.atr_long_period = atr_long_period;
        self
    }

    /// Customise detection thresholds.
    ///
    /// - `adx_threshold`: ADX value above which market is Trending (default 25.0)
    /// - `chop_threshold`: Chop value above which market is Ranging (default 61.8)
    /// - `vol_high_ratio`: ATR ratio above which volatility is High (default 1.3)
    /// - `vol_low_ratio`: ATR ratio below which volatility is Low (default 0.7)
    pub fn with_thresholds(
        mut self,
        adx_threshold: f64,
        chop_threshold: f64,
        vol_high_ratio: f64,
        vol_low_ratio: f64,
    ) -> Self {
        self.adx_threshold = adx_threshold;
        self.chop_threshold = chop_threshold;
        self.vol_high_ratio = vol_high_ratio;
        self.vol_low_ratio = vol_low_ratio;
        self
    }

    /// Feed one bar. Returns `Some(RegimeState)` once all indicators are warm.
    /// ADX and Chop both require `period` bars, so the detector is ready after ~14 bars.
    pub fn update(&mut self, bar: &Bar) -> Option<RegimeState> {
        let adx_val = self.adx.update(bar.high, bar.low, bar.close);
        let chop_val = self.chop.update(bar.high, bar.low, bar.close);
        let atr_short_val = self.atr_short.update(bar.high, bar.low, bar.close);
        let atr_long_val = self.atr_long.update(bar.high, bar.low, bar.close);

        // Both trend indicators must be ready before emitting a regime.
        let adx = adx_val?.adx;
        let chop = chop_val?;

        // ── Trend regime ──────────────────────────────────────────────────────
        // ADX takes precedence: strong trend overrides choppiness reading.
        let trend = if adx > self.adx_threshold {
            TrendRegime::Trending
        } else if chop > self.chop_threshold {
            TrendRegime::Ranging
        } else {
            TrendRegime::Neutral
        };

        // ── Volatility regime ─────────────────────────────────────────────────
        // Only classified once the long ATR baseline is established.
        let volatility = match (atr_short_val, atr_long_val) {
            (Some(s), Some(l)) if l.atr > f64::EPSILON => {
                let ratio = s.atr / l.atr;
                if ratio > self.vol_high_ratio {
                    VolRegime::High
                } else if ratio < self.vol_low_ratio {
                    VolRegime::Low
                } else {
                    VolRegime::Normal
                }
            }
            // Long ATR not yet ready — treat as normal vol.
            _ => VolRegime::Normal,
        };

        Some(RegimeState::new(trend, volatility))
    }

    /// Reset all internal indicator state (for batch optimization reuse).
    pub fn reset(&mut self) {
        self.adx = Adx::new(self.adx_period);
        self.chop = Chop::new(self.chop_period);
        self.atr_short = Atr::new(self.atr_short_period);
        self.atr_long = Atr::new(self.atr_long_period);
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
        // Feed fewer bars than the ADX period (14) — should return None.
        for i in 0..13 {
            let result = det.update(&bar(i, 100.0, 101.0, 99.0, 100.0));
            assert!(result.is_none(), "bar {i} should still be in warm-up");
        }
    }

    #[test]
    fn trending_regime_high_adx() {
        let mut det = RegimeDetector::new();
        // Strong uptrend: each bar higher high/close, low deviation → high ADX.
        for i in 0..60 {
            let base = 100.0 + i as f64;
            det.update(&bar(i, base, base + 2.0, base - 0.5, base + 1.5));
        }
        let regime = det.update(&bar(60, 160.0, 162.0, 159.5, 161.5)).unwrap();
        assert_eq!(regime.trend, TrendRegime::Trending);
    }

    #[test]
    fn ranging_regime_choppy() {
        let mut det = RegimeDetector::new();
        // Oscillating market: alternating up/down bars → high Chop, low ADX.
        for i in 0..60 {
            let (h, l, c) = if i % 2 == 0 {
                (102.0, 98.0, 101.0)
            } else {
                (102.0, 98.0, 99.0)
            };
            det.update(&bar(i, 100.0, h, l, c));
        }
        let regime = det.update(&bar(60, 100.0, 102.0, 98.0, 100.0)).unwrap();
        // Choppy market should be Ranging or at least not strongly Trending.
        assert_ne!(regime.trend, TrendRegime::Trending);
    }

    #[test]
    fn reset_clears_state() {
        let mut det = RegimeDetector::new();
        for i in 0..30 {
            let base = 100.0 + i as f64;
            det.update(&bar(i, base, base + 1.0, base - 1.0, base));
        }
        det.reset();
        // After reset the detector should be in warm-up again.
        let result = det.update(&bar(0, 100.0, 101.0, 99.0, 100.0));
        assert!(result.is_none());
    }
}
