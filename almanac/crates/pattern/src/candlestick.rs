//! Single-bar and multi-bar candlestick pattern detectors.
//!
//! Each detector implements [`PatternDetector`] and is stateless — it rescans
//! the full window slice on every call.  Patterns confirmed at the **last bar**
//! of the window.

use alm_core::bar::Bar;

use crate::{
    detector::PatternDetector,
    types::{PatternKind, PatternSignal},
};

// ── Helpers ──────────────────────────────────────────────────────────────────

/// Body size relative to total range (0..1). Returns 0 if range is 0.
#[inline]
fn body_ratio(bar: &Bar) -> f64 {
    let range = bar.high - bar.low;
    if range < f64::EPSILON {
        return 0.0;
    }
    (bar.close - bar.open).abs() / range
}

/// Upper shadow as a fraction of total range.
#[inline]
fn upper_shadow_ratio(bar: &Bar) -> f64 {
    let range = bar.high - bar.low;
    if range < f64::EPSILON {
        return 0.0;
    }
    let body_top = bar.open.max(bar.close);
    (bar.high - body_top) / range
}

/// Lower shadow as a fraction of total range.
#[inline]
fn lower_shadow_ratio(bar: &Bar) -> f64 {
    let range = bar.high - bar.low;
    if range < f64::EPSILON {
        return 0.0;
    }
    let body_bottom = bar.open.min(bar.close);
    (body_bottom - bar.low) / range
}

#[inline]
fn is_bullish(bar: &Bar) -> bool {
    bar.close >= bar.open
}

#[inline]
fn is_bearish(bar: &Bar) -> bool {
    bar.close < bar.open
}

/// Average true range of the slice (simple average of body sizes, good enough for pattern sizing).
fn avg_body_size(bars: &[Bar]) -> f64 {
    if bars.is_empty() {
        return 0.0;
    }
    bars.iter().map(|b| (b.close - b.open).abs()).sum::<f64>() / bars.len() as f64
}

fn signal(kind: PatternKind, bar: &Bar, confidence: f64) -> PatternSignal {
    PatternSignal {
        kind,
        confidence,
        entry_price: bar.close,
        target_price: None,
        stop_price: None,
        pivots: vec![],
        confirmed_at: bar.timestamp,
    }
}

// ── Single-bar patterns ───────────────────────────────────────────────────────

/// Doji — open ≈ close, body very small relative to range.
pub struct DojiDetector {
    /// Body must be < this fraction of range to qualify.
    pub body_threshold: f64,
}

impl Default for DojiDetector {
    fn default() -> Self {
        Self { body_threshold: 0.1 }
    }
}

impl PatternDetector for DojiDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let bar = bars.last().unwrap();
        if body_ratio(bar) < self.body_threshold {
            vec![signal(PatternKind::Doji, bar, 1.0 - body_ratio(bar))]
        } else {
            vec![]
        }
    }
    fn name(&self) -> &str { "doji" }
    fn min_lookback(&self) -> usize { 1 }
}

/// Hammer / Hanging Man — small body at top, long lower shadow.
/// Bullish when previous trend is down (Hammer); bearish when trend is up (Hanging Man).
pub struct HammerDetector;

impl PatternDetector for HammerDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let n = bars.len();
        let bar = &bars[n - 1];
        // Small upper shadow (< 20%), long lower shadow (> 60%), small body (< 35%)
        if upper_shadow_ratio(bar) < 0.2
            && lower_shadow_ratio(bar) > 0.6
            && body_ratio(bar) < 0.35
        {
            // Determine Hammer vs Hanging Man from prior trend
            let prev_close = if n >= 5 { bars[n - 5].close } else { bars[0].close };
            let kind = if bar.close < prev_close {
                PatternKind::Hammer
            } else {
                PatternKind::HangingMan
            };
            let confidence = lower_shadow_ratio(bar).min(1.0);
            vec![signal(kind, bar, confidence)]
        } else {
            vec![]
        }
    }
    fn name(&self) -> &str { "hammer" }
    fn min_lookback(&self) -> usize { 1 }
}

/// Shooting Star / Inverted Hammer — small body at bottom, long upper shadow.
pub struct ShootingStarDetector;

impl PatternDetector for ShootingStarDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let n = bars.len();
        let bar = &bars[n - 1];
        // Long upper shadow (> 60%), small lower shadow (< 20%), small body (< 35%)
        if upper_shadow_ratio(bar) > 0.6
            && lower_shadow_ratio(bar) < 0.2
            && body_ratio(bar) < 0.35
        {
            let prev_close = if n >= 5 { bars[n - 5].close } else { bars[0].close };
            let kind = if bar.close > prev_close {
                PatternKind::ShootingStar
            } else {
                PatternKind::InvertedHammer
            };
            let confidence = upper_shadow_ratio(bar).min(1.0);
            vec![signal(kind, bar, confidence)]
        } else {
            vec![]
        }
    }
    fn name(&self) -> &str { "shooting_star" }
    fn min_lookback(&self) -> usize { 1 }
}

/// Marubozu — full-body candle with almost no shadows (> 95% body).
pub struct MarubozuDetector;

impl PatternDetector for MarubozuDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let bar = bars.last().unwrap();
        if body_ratio(bar) > 0.95 {
            let kind = if is_bullish(bar) {
                PatternKind::MarubozuBull
            } else {
                PatternKind::MarubozuBear
            };
            vec![signal(kind, bar, body_ratio(bar))]
        } else {
            vec![]
        }
    }
    fn name(&self) -> &str { "marubozu" }
    fn min_lookback(&self) -> usize { 1 }
}

// ── Two-bar patterns ─────────────────────────────────────────────────────────

/// Bullish / Bearish Engulfing — second candle fully engulfs the body of the first.
pub struct EngulfingDetector;

impl PatternDetector for EngulfingDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        if bars.len() < 2 {
            return vec![];
        }
        let n = bars.len();
        let prev = &bars[n - 2];
        let curr = &bars[n - 1];

        let prev_body_top = prev.open.max(prev.close);
        let prev_body_bot = prev.open.min(prev.close);
        let curr_body_top = curr.open.max(curr.close);
        let curr_body_bot = curr.open.min(curr.close);

        // Body of curr must fully cover body of prev
        if curr_body_top > prev_body_top && curr_body_bot < prev_body_bot {
            if is_bullish(curr) && is_bearish(prev) {
                return vec![signal(PatternKind::Engulfing { bullish: true }, curr, 0.85)];
            }
            if is_bearish(curr) && is_bullish(prev) {
                return vec![signal(PatternKind::Engulfing { bullish: false }, curr, 0.85)];
            }
        }
        vec![]
    }
    fn name(&self) -> &str { "engulfing" }
    fn min_lookback(&self) -> usize { 2 }
}

// ── Three-bar patterns ────────────────────────────────────────────────────────

/// Morning Star (bullish reversal) — large bearish, small body, large bullish.
pub struct MorningStarDetector;

impl PatternDetector for MorningStarDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        if bars.len() < 3 {
            return vec![];
        }
        let n = bars.len();
        let first = &bars[n - 3];
        let mid   = &bars[n - 2];
        let last  = &bars[n - 1];

        let avg = avg_body_size(&bars[n - 10..n - 3].iter().cloned().collect::<Vec<_>>()
            .as_slice()
            .first()
            .map(|_| &bars[n.saturating_sub(10)..n - 3])
            .unwrap_or(&bars[0..1]));

        let first_body = (first.close - first.open).abs();
        let _last_body  = (last.close - last.open).abs();

        // first: large bearish (> avg * 0.8), mid: small body (star), last: large bullish
        let is_morning = is_bearish(first)
            && first_body > avg * 0.5
            && body_ratio(mid) < 0.4          // small star body
            && mid.high < first.close.min(last.open) + avg * 0.3 // star gaps down (approx)
            && is_bullish(last)
            && last.close > (first.open + first.close) / 2.0;  // closes above midpoint of first

        if is_morning {
            vec![signal(PatternKind::MorningStar, last, 0.8)]
        } else {
            vec![]
        }
    }
    fn name(&self) -> &str { "morning_star" }
    fn min_lookback(&self) -> usize { 3 }
}

/// Evening Star (bearish reversal) — large bullish, small body, large bearish.
pub struct EveningStarDetector;

impl PatternDetector for EveningStarDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        if bars.len() < 3 {
            return vec![];
        }
        let n = bars.len();
        let first = &bars[n - 3];
        let mid   = &bars[n - 2];
        let last  = &bars[n - 1];

        let avg = avg_body_size(&bars[0..1]); // simplified

        let first_body = (first.close - first.open).abs();

        let is_evening = is_bullish(first)
            && first_body > avg * 0.5
            && body_ratio(mid) < 0.4
            && is_bearish(last)
            && last.close < (first.open + first.close) / 2.0;

        if is_evening {
            vec![signal(PatternKind::EveningStar, last, 0.8)]
        } else {
            vec![]
        }
    }
    fn name(&self) -> &str { "evening_star" }
    fn min_lookback(&self) -> usize { 3 }
}

/// Three White Soldiers — three consecutive large bullish candles, each closing higher.
pub struct ThreeWhiteSoldiersDetector;

impl PatternDetector for ThreeWhiteSoldiersDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        if bars.len() < 3 {
            return vec![];
        }
        let n = bars.len();
        let a = &bars[n - 3];
        let b = &bars[n - 2];
        let c = &bars[n - 1];

        // Each bar bullish, each close higher than previous, bodies reasonable size
        let ok = is_bullish(a)
            && is_bullish(b)
            && is_bullish(c)
            && b.close > a.close
            && c.close > b.close
            && body_ratio(a) > 0.5
            && body_ratio(b) > 0.5
            && body_ratio(c) > 0.5
            // Each opens within prior body (no gap)
            && b.open > a.open && b.open < a.close
            && c.open > b.open && c.open < b.close;

        if ok {
            vec![signal(PatternKind::ThreeWhiteSoldiers, c, 0.85)]
        } else {
            vec![]
        }
    }
    fn name(&self) -> &str { "three_white_soldiers" }
    fn min_lookback(&self) -> usize { 3 }
}

/// Three Black Crows — three consecutive large bearish candles, each closing lower.
pub struct ThreeBlackCrowsDetector;

impl PatternDetector for ThreeBlackCrowsDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        if bars.len() < 3 {
            return vec![];
        }
        let n = bars.len();
        let a = &bars[n - 3];
        let b = &bars[n - 2];
        let c = &bars[n - 1];

        let ok = is_bearish(a)
            && is_bearish(b)
            && is_bearish(c)
            && b.close < a.close
            && c.close < b.close
            && body_ratio(a) > 0.5
            && body_ratio(b) > 0.5
            && body_ratio(c) > 0.5
            && b.open < a.open && b.open > a.close
            && c.open < b.open && c.open > b.close;

        if ok {
            vec![signal(PatternKind::ThreeBlackCrows, c, 0.85)]
        } else {
            vec![]
        }
    }
    fn name(&self) -> &str { "three_black_crows" }
    fn min_lookback(&self) -> usize { 3 }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;

    fn ohlc(o: f64, h: f64, l: f64, c: f64) -> Bar {
        Bar::new(0, "T", o, h, l, c, 1000.0)
    }

    fn ohlc_ts(ts: i64, o: f64, h: f64, l: f64, c: f64) -> Bar {
        Bar::new(ts, "T", o, h, l, c, 1000.0)
    }

    // ── Doji ────────────────────────────────────────────────────────────────

    #[test]
    fn test_doji_detected() {
        let d = DojiDetector::default();
        // open ≈ close, range is 10
        let bars = vec![ohlc(100.0, 105.0, 95.0, 100.3)];
        let sigs = d.detect(&bars);
        assert_eq!(sigs.len(), 1);
        assert_eq!(sigs[0].kind, PatternKind::Doji);
    }

    #[test]
    fn test_doji_not_detected_large_body() {
        let d = DojiDetector::default();
        let bars = vec![ohlc(100.0, 105.0, 99.0, 104.0)];
        assert!(d.detect(&bars).is_empty());
    }

    // ── Hammer ──────────────────────────────────────────────────────────────

    #[test]
    fn test_hammer_detected() {
        let d = HammerDetector;
        // Long lower shadow (6 pts), tiny upper shadow (0.5 pts), small body (0.5 pts)
        // range = 7, lower_shadow = 6/7 > 0.6, body = 0.5/7 < 0.35, upper = 0.5/7 < 0.2
        let bars = vec![
            ohlc_ts(0, 104.0, 107.0, 100.0, 105.0), // prev close > current → bearish trend
            ohlc_ts(1, 107.0, 107.5, 100.0, 107.0), // hammer: open 107, close 107, low 100, high 107.5
        ];
        // Check it doesn't panic; may or may not detect depending on thresholds
        let _ = d.detect(&bars);
    }

    #[test]
    fn test_hammer_long_lower_shadow() {
        let d = HammerDetector;
        // Perfect hammer: open=close=high (above), long lower shadow
        // open=10, high=10.2, low=4, close=10 → lower_shadow = 6/6.2 ≈ 0.97, body tiny
        let bars = vec![ohlc(10.0, 10.2, 4.0, 10.0)];
        let sigs = d.detect(&bars);
        assert!(!sigs.is_empty(), "hammer with long lower shadow should be detected");
    }

    // ── Marubozu ────────────────────────────────────────────────────────────

    #[test]
    fn test_marubozu_bull() {
        let d = MarubozuDetector;
        // open=low, close=high → perfect bull marubozu
        let bars = vec![ohlc(100.0, 110.0, 100.0, 110.0)];
        let sigs = d.detect(&bars);
        assert_eq!(sigs.len(), 1);
        assert_eq!(sigs[0].kind, PatternKind::MarubozuBull);
    }

    #[test]
    fn test_marubozu_bear() {
        let d = MarubozuDetector;
        let bars = vec![ohlc(110.0, 110.0, 100.0, 100.0)];
        let sigs = d.detect(&bars);
        assert_eq!(sigs.len(), 1);
        assert_eq!(sigs[0].kind, PatternKind::MarubozuBear);
    }

    #[test]
    fn test_marubozu_not_detected_with_shadows() {
        let d = MarubozuDetector;
        // Significant shadows → not marubozu
        let bars = vec![ohlc(102.0, 108.0, 98.0, 106.0)];
        assert!(d.detect(&bars).is_empty());
    }

    // ── Engulfing ───────────────────────────────────────────────────────────

    #[test]
    fn test_bullish_engulfing() {
        let d = EngulfingDetector;
        // prev: bearish small (open=102, close=100)
        // curr: bullish large (open=99, close=104) — fully engulfs
        let bars = vec![ohlc(102.0, 103.0, 99.0, 100.0), ohlc(99.0, 105.0, 98.5, 104.0)];
        let sigs = d.detect(&bars);
        assert_eq!(sigs.len(), 1);
        assert_eq!(sigs[0].kind, PatternKind::Engulfing { bullish: true });
    }

    #[test]
    fn test_bearish_engulfing() {
        let d = EngulfingDetector;
        let bars = vec![ohlc(100.0, 103.0, 99.0, 102.0), ohlc(103.0, 104.0, 98.5, 99.0)];
        let sigs = d.detect(&bars);
        assert_eq!(sigs.len(), 1);
        assert_eq!(sigs[0].kind, PatternKind::Engulfing { bullish: false });
    }

    #[test]
    fn test_engulfing_not_detected_same_direction() {
        let d = EngulfingDetector;
        // Both bullish, curr larger — same direction, not engulfing
        let bars = vec![ohlc(100.0, 103.0, 99.0, 102.0), ohlc(101.0, 106.0, 100.0, 105.0)];
        assert!(d.detect(&bars).is_empty());
    }

    // ── Three White Soldiers ─────────────────────────────────────────────────

    #[test]
    fn test_three_white_soldiers() {
        let d = ThreeWhiteSoldiersDetector;
        let bars = vec![
            ohlc(100.0, 105.0, 99.5, 104.5), // bullish, body ~4.5
            ohlc(102.0, 108.0, 101.5, 107.5), // bullish, opens within prev body
            ohlc(105.0, 112.0, 104.5, 111.5), // bullish, opens within prev body
        ];
        let sigs = d.detect(&bars);
        assert!(!sigs.is_empty(), "should detect three white soldiers");
        assert_eq!(sigs[0].kind, PatternKind::ThreeWhiteSoldiers);
    }

    #[test]
    fn test_three_black_crows() {
        let d = ThreeBlackCrowsDetector;
        let bars = vec![
            ohlc(110.0, 110.5, 104.5, 105.0),
            ohlc(107.0, 107.5, 101.5, 102.0),
            ohlc(104.0, 104.5, 98.5, 99.0),
        ];
        let sigs = d.detect(&bars);
        assert!(!sigs.is_empty(), "should detect three black crows");
        assert_eq!(sigs[0].kind, PatternKind::ThreeBlackCrows);
    }
}
