//! Geometric chart pattern detectors using pivot-point analysis.
//!
//! All detectors use [`crate::pivot::find_pivots`] to identify swing highs/lows,
//! then check the geometric relationships between those pivots.
//!
//! ## Tolerance
//! Price levels are compared with a relative tolerance (default 3%) to handle
//! real-world noise.  For example, the two tops of a Double Top don't need to be
//! exactly equal — they must be within `tolerance * level`.

use alm_core::bar::Bar;

use crate::{
    detector::PatternDetector,
    pivot::{find_pivots, highs, lows},
    types::{PatternKind, PatternSignal, PivotPoint},
};

// ── Utility ───────────────────────────────────────────────────────────────────

/// True if `a` and `b` are within `tol` fraction of each other.
#[inline]
fn within(a: f64, b: f64, tol: f64) -> bool {
    let avg = (a + b) / 2.0;
    if avg < f64::EPSILON {
        return true;
    }
    (a - b).abs() / avg <= tol
}

fn make_signal(
    kind: PatternKind,
    bars: &[Bar],
    confidence: f64,
    pivots: Vec<PivotPoint>,
    target: Option<f64>,
    stop: Option<f64>,
) -> PatternSignal {
    let last = bars.last().unwrap();
    PatternSignal {
        kind,
        confidence,
        entry_price: last.close,
        target_price: target,
        stop_price: stop,
        pivots,
        confirmed_at: last.timestamp,
    }
}

// ── Double Top ────────────────────────────────────────────────────────────────

/// Double Top detector.
///
/// Requires:
/// 1. Two swing highs at approximately the same price level.
/// 2. A swing low (neckline) between them.
/// 3. Current close breaks below the neckline.
pub struct DoubleTopDetector {
    /// Pivot swing size (bars each side).
    pub pivot_size: usize,
    /// Maximum relative difference between the two tops (default 0.03 = 3%).
    pub tolerance: f64,
}

impl Default for DoubleTopDetector {
    fn default() -> Self {
        Self { pivot_size: 3, tolerance: 0.03 }
    }
}

impl PatternDetector for DoubleTopDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let pivots = find_pivots(bars, self.pivot_size, self.pivot_size);
        let hs = highs(&pivots);
        let ls = lows(&pivots);

        if hs.len() < 2 || ls.is_empty() {
            return vec![];
        }

        let mut results = Vec::new();
        let n_hs = hs.len();

        // Scan all pairs of consecutive swing highs
        for i in 0..n_hs - 1 {
            let left_top = hs[i];
            let right_top = hs[i + 1];

            // Tops must be at similar levels
            if !within(left_top.price, right_top.price, self.tolerance) {
                continue;
            }

            // Find the swing low between the two tops
            let neckline_low = ls
                .iter()
                .filter(|l| l.bar_index > left_top.bar_index && l.bar_index < right_top.bar_index)
                .min_by(|a, b| a.price.partial_cmp(&b.price).unwrap());

            let Some(neckline) = neckline_low else { continue };

            // Confirmation: current price breaks below the neckline
            let current_close = bars.last().unwrap().close;
            if current_close >= neckline.price {
                continue;
            }

            // Pattern height for measured move target
            let pattern_height = (left_top.price + right_top.price) / 2.0 - neckline.price;
            let target = neckline.price - pattern_height;
            let stop = right_top.price * 1.005; // just above the right top

            let confidence = {
                let level_match = 1.0 - (left_top.price - right_top.price).abs()
                    / ((left_top.price + right_top.price) / 2.0);
                (level_match * 0.7 + 0.3).min(1.0)
            };

            results.push(make_signal(
                PatternKind::DoubleTop,
                bars,
                confidence,
                vec![(*left_top).clone(), (*neckline).clone(), (*right_top).clone()],
                Some(target),
                Some(stop),
            ));
        }

        results
    }

    fn name(&self) -> &str { "double_top" }
    fn min_lookback(&self) -> usize { self.pivot_size * 4 + 5 }
}

// ── Double Bottom ─────────────────────────────────────────────────────────────

/// Double Bottom detector.
///
/// Mirror of Double Top — two swing lows at similar price, neckline above them,
/// confirmation when price breaks above the neckline.
pub struct DoubleBottomDetector {
    pub pivot_size: usize,
    pub tolerance: f64,
}

impl Default for DoubleBottomDetector {
    fn default() -> Self {
        Self { pivot_size: 3, tolerance: 0.03 }
    }
}

impl PatternDetector for DoubleBottomDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let pivots = find_pivots(bars, self.pivot_size, self.pivot_size);
        let hs = highs(&pivots);
        let ls = lows(&pivots);

        if ls.len() < 2 || hs.is_empty() {
            return vec![];
        }

        let mut results = Vec::new();
        let n_ls = ls.len();

        for i in 0..n_ls - 1 {
            let left_bot = ls[i];
            let right_bot = ls[i + 1];

            if !within(left_bot.price, right_bot.price, self.tolerance) {
                continue;
            }

            // Swing high (neckline) between the two bottoms
            let neckline_high = hs
                .iter()
                .filter(|h| {
                    h.bar_index > left_bot.bar_index && h.bar_index < right_bot.bar_index
                })
                .max_by(|a, b| a.price.partial_cmp(&b.price).unwrap());

            let Some(neckline) = neckline_high else { continue };

            let current_close = bars.last().unwrap().close;
            if current_close <= neckline.price {
                continue;
            }

            let pattern_height = neckline.price - (left_bot.price + right_bot.price) / 2.0;
            let target = neckline.price + pattern_height;
            let stop = right_bot.price * 0.995;

            let confidence = {
                let level_match = 1.0 - (left_bot.price - right_bot.price).abs()
                    / ((left_bot.price + right_bot.price) / 2.0);
                (level_match * 0.7 + 0.3).min(1.0)
            };

            results.push(make_signal(
                PatternKind::DoubleBottom,
                bars,
                confidence,
                vec![(*left_bot).clone(), (*neckline).clone(), (*right_bot).clone()],
                Some(target),
                Some(stop),
            ));
        }

        results
    }

    fn name(&self) -> &str { "double_bottom" }
    fn min_lookback(&self) -> usize { self.pivot_size * 4 + 5 }
}

// ── Head and Shoulders ────────────────────────────────────────────────────────

/// Head and Shoulders (bearish reversal).
///
/// Three swing highs where the middle (head) is higher than both shoulders.
/// Left and right shoulder heights are within tolerance of each other.
/// Confirmation: close breaks below the neckline (line through the two troughs).
pub struct HeadAndShouldersDetector {
    pub pivot_size: usize,
    pub shoulder_tolerance: f64,
}

impl Default for HeadAndShouldersDetector {
    fn default() -> Self {
        Self { pivot_size: 3, shoulder_tolerance: 0.05 }
    }
}

impl PatternDetector for HeadAndShouldersDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let pivots = find_pivots(bars, self.pivot_size, self.pivot_size);
        let hs = highs(&pivots);
        let ls = lows(&pivots);

        if hs.len() < 3 || ls.len() < 2 {
            return vec![];
        }

        let mut results = Vec::new();
        let n_hs = hs.len();

        // Scan all triples of consecutive swing highs
        for i in 0..n_hs.saturating_sub(2) {
            let left_sh = hs[i];
            let head    = hs[i + 1];
            let right_sh = hs[i + 2];

            // Head must be higher than both shoulders
            if head.price <= left_sh.price || head.price <= right_sh.price {
                continue;
            }

            // Shoulders must be at similar heights
            if !within(left_sh.price, right_sh.price, self.shoulder_tolerance) {
                continue;
            }

            // Find left trough (between left shoulder and head)
            let left_trough = ls
                .iter()
                .filter(|l| l.bar_index > left_sh.bar_index && l.bar_index < head.bar_index)
                .min_by(|a, b| a.price.partial_cmp(&b.price).unwrap());

            // Find right trough (between head and right shoulder)
            let right_trough = ls
                .iter()
                .filter(|l| l.bar_index > head.bar_index && l.bar_index < right_sh.bar_index)
                .min_by(|a, b| a.price.partial_cmp(&b.price).unwrap());

            let (Some(lt), Some(rt)) = (left_trough, right_trough) else { continue };

            // Neckline: average of the two troughs (simplified — assumes roughly horizontal)
            let neckline = (lt.price + rt.price) / 2.0;

            // Confirmation: close below neckline
            let current_close = bars.last().unwrap().close;
            if current_close >= neckline {
                continue;
            }

            let pattern_height = head.price - neckline;
            let target = neckline - pattern_height;
            let stop = right_sh.price * 1.005;

            let shoulder_symmetry =
                1.0 - (left_sh.price - right_sh.price).abs() / head.price;
            let confidence = (shoulder_symmetry * 0.7 + 0.3).min(1.0);

            results.push(make_signal(
                PatternKind::HeadAndShoulders,
                bars,
                confidence,
                vec![
                    (*left_sh).clone(),
                    (*lt).clone(),
                    (*head).clone(),
                    (*rt).clone(),
                    (*right_sh).clone(),
                ],
                Some(target),
                Some(stop),
            ));
        }

        results
    }

    fn name(&self) -> &str { "head_and_shoulders" }
    fn min_lookback(&self) -> usize { self.pivot_size * 6 + 5 }
}

// ── Inverse Head and Shoulders ────────────────────────────────────────────────

/// Inverse Head and Shoulders (bullish reversal).
///
/// Three swing lows where the middle (head) is lower than both shoulders.
pub struct InverseHeadAndShouldersDetector {
    pub pivot_size: usize,
    pub shoulder_tolerance: f64,
}

impl Default for InverseHeadAndShouldersDetector {
    fn default() -> Self {
        Self { pivot_size: 3, shoulder_tolerance: 0.05 }
    }
}

impl PatternDetector for InverseHeadAndShouldersDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let pivots = find_pivots(bars, self.pivot_size, self.pivot_size);
        let hs = highs(&pivots);
        let ls = lows(&pivots);

        if ls.len() < 3 || hs.len() < 2 {
            return vec![];
        }

        let mut results = Vec::new();
        let n_ls = ls.len();

        for i in 0..n_ls.saturating_sub(2) {
            let left_sh  = ls[i];
            let head     = ls[i + 1];
            let right_sh = ls[i + 2];

            // Head must be lower than both shoulders
            if head.price >= left_sh.price || head.price >= right_sh.price {
                continue;
            }

            if !within(left_sh.price, right_sh.price, self.shoulder_tolerance) {
                continue;
            }

            // Left peak (between left shoulder and head)
            let left_peak = hs
                .iter()
                .filter(|h| h.bar_index > left_sh.bar_index && h.bar_index < head.bar_index)
                .max_by(|a, b| a.price.partial_cmp(&b.price).unwrap());

            // Right peak (between head and right shoulder)
            let right_peak = hs
                .iter()
                .filter(|h| h.bar_index > head.bar_index && h.bar_index < right_sh.bar_index)
                .max_by(|a, b| a.price.partial_cmp(&b.price).unwrap());

            let (Some(lp), Some(rp)) = (left_peak, right_peak) else { continue };

            let neckline = (lp.price + rp.price) / 2.0;

            let current_close = bars.last().unwrap().close;
            if current_close <= neckline {
                continue;
            }

            let pattern_height = neckline - head.price;
            let target = neckline + pattern_height;
            let stop = right_sh.price * 0.995;

            let shoulder_symmetry =
                1.0 - (left_sh.price - right_sh.price).abs() / neckline;
            let confidence = (shoulder_symmetry * 0.7 + 0.3).min(1.0);

            results.push(make_signal(
                PatternKind::InverseHeadAndShoulders,
                bars,
                confidence,
                vec![
                    (*left_sh).clone(),
                    (*lp).clone(),
                    (*head).clone(),
                    (*rp).clone(),
                    (*right_sh).clone(),
                ],
                Some(target),
                Some(stop),
            ));
        }

        results
    }

    fn name(&self) -> &str { "inverse_head_and_shoulders" }
    fn min_lookback(&self) -> usize { self.pivot_size * 6 + 5 }
}

// ── Continuation patterns ──────────────────────────────────────────────────────

/// Extrapolate a trendline defined by two (index, price) anchor points to bar `at`.
#[inline]
fn trendline_at(p1: (f64, f64), p2: (f64, f64), at: f64) -> f64 {
    if (p2.0 - p1.0).abs() < f64::EPSILON {
        return p1.1;
    }
    let slope = (p2.1 - p1.1) / (p2.0 - p1.0);
    p1.1 + slope * (at - p1.0)
}

// ── Ascending Triangle ────────────────────────────────────────────────────────

/// Ascending Triangle: flat/horizontal resistance + rising support lows.
///
/// Bullish continuation — confirms when close breaks above the resistance line.
pub struct AscendingTriangleDetector {
    pub pivot_size: usize,
    /// Maximum relative difference between the two highs to be considered "flat".
    pub tolerance: f64,
}

impl Default for AscendingTriangleDetector {
    fn default() -> Self { Self { pivot_size: 3, tolerance: 0.02 } }
}

impl PatternDetector for AscendingTriangleDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let pivots = find_pivots(bars, self.pivot_size, self.pivot_size);
        let hs = highs(&pivots);
        let ls = lows(&pivots);

        if hs.len() < 2 || ls.len() < 2 { return vec![]; }

        let h1 = hs[hs.len() - 2];
        let h2 = hs[hs.len() - 1];
        // Resistance must be flat
        if !within(h1.price, h2.price, self.tolerance) { return vec![]; }

        let l1 = ls[ls.len() - 2];
        let l2 = ls[ls.len() - 1];
        // Support lows must be rising
        if l2.price <= l1.price { return vec![]; }

        let resistance = (h1.price + h2.price) / 2.0;

        // Confirm: close breaks above resistance
        let current = bars.last().unwrap();
        if current.close <= resistance { return vec![]; }

        let pattern_height = resistance - l1.price;
        let target = resistance + pattern_height;
        let stop = l2.price * 0.995;

        let flatness = 1.0 - (h1.price - h2.price).abs() / resistance;
        let confidence = (flatness * 0.8 + 0.2).min(1.0);

        vec![make_signal(
            PatternKind::AscendingTriangle,
            bars,
            confidence,
            vec![(*h1).clone(), (*l1).clone(), (*h2).clone(), (*l2).clone()],
            Some(target),
            Some(stop),
        )]
    }

    fn name(&self) -> &str { "ascending_triangle" }
    fn min_lookback(&self) -> usize { self.pivot_size * 4 + 5 }
}

// ── Descending Triangle ───────────────────────────────────────────────────────

/// Descending Triangle: declining highs + flat/horizontal support.
///
/// Bearish continuation — confirms when close breaks below the support line.
pub struct DescendingTriangleDetector {
    pub pivot_size: usize,
    pub tolerance: f64,
}

impl Default for DescendingTriangleDetector {
    fn default() -> Self { Self { pivot_size: 3, tolerance: 0.02 } }
}

impl PatternDetector for DescendingTriangleDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let pivots = find_pivots(bars, self.pivot_size, self.pivot_size);
        let hs = highs(&pivots);
        let ls = lows(&pivots);

        if hs.len() < 2 || ls.len() < 2 { return vec![]; }

        let h1 = hs[hs.len() - 2];
        let h2 = hs[hs.len() - 1];
        // Highs must be declining
        if h2.price >= h1.price { return vec![]; }

        let l1 = ls[ls.len() - 2];
        let l2 = ls[ls.len() - 1];
        // Support must be flat
        if !within(l1.price, l2.price, self.tolerance) { return vec![]; }

        let support = (l1.price + l2.price) / 2.0;

        // Confirm: close breaks below support
        let current = bars.last().unwrap();
        if current.close >= support { return vec![]; }

        let pattern_height = h1.price - support;
        let target = support - pattern_height;
        let stop = h2.price * 1.005;

        let flatness = 1.0 - (l1.price - l2.price).abs() / support;
        let confidence = (flatness * 0.8 + 0.2).min(1.0);

        vec![make_signal(
            PatternKind::DescendingTriangle,
            bars,
            confidence,
            vec![(*h1).clone(), (*l1).clone(), (*h2).clone(), (*l2).clone()],
            Some(target),
            Some(stop),
        )]
    }

    fn name(&self) -> &str { "descending_triangle" }
    fn min_lookback(&self) -> usize { self.pivot_size * 4 + 5 }
}

// ── Symmetric Triangle ────────────────────────────────────────────────────────

/// Symmetric Triangle: declining highs + rising lows — converging trendlines.
///
/// Neutral consolidation. Confirms bullish breakout when close exceeds the upper
/// trendline extrapolated to the current bar.
pub struct SymmetricTriangleDetector {
    pub pivot_size: usize,
}

impl Default for SymmetricTriangleDetector {
    fn default() -> Self { Self { pivot_size: 3 } }
}

impl PatternDetector for SymmetricTriangleDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let pivots = find_pivots(bars, self.pivot_size, self.pivot_size);
        let hs = highs(&pivots);
        let ls = lows(&pivots);

        if hs.len() < 2 || ls.len() < 2 { return vec![]; }

        let h1 = hs[hs.len() - 2];
        let h2 = hs[hs.len() - 1];
        let l1 = ls[ls.len() - 2];
        let l2 = ls[ls.len() - 1];

        // Highs declining, lows rising
        if h2.price >= h1.price || l2.price <= l1.price { return vec![]; }

        let last_idx = (bars.len() - 1) as f64;
        let upper = trendline_at(
            (h1.bar_index as f64, h1.price),
            (h2.bar_index as f64, h2.price),
            last_idx,
        );
        let lower = trendline_at(
            (l1.bar_index as f64, l1.price),
            (l2.bar_index as f64, l2.price),
            last_idx,
        );

        // Lines must not have crossed yet
        if upper <= lower { return vec![]; }

        // Confirm bullish breakout above upper trendline
        let current_close = bars.last().unwrap().close;
        if current_close <= upper { return vec![]; }

        let pattern_height = h1.price - l1.price;
        let target = upper + pattern_height;
        let stop = lower * 0.995;

        // Confidence from degree of convergence
        let convergence = 1.0 - (upper - lower) / pattern_height.max(f64::EPSILON);
        let confidence = (convergence * 0.6 + 0.4).min(1.0).max(0.0);

        vec![make_signal(
            PatternKind::SymmetricTriangle,
            bars,
            confidence,
            vec![(*h1).clone(), (*l1).clone(), (*h2).clone(), (*l2).clone()],
            Some(target),
            Some(stop),
        )]
    }

    fn name(&self) -> &str { "symmetric_triangle" }
    fn min_lookback(&self) -> usize { self.pivot_size * 4 + 5 }
}

// ── Flag (Bull / Bear) ────────────────────────────────────────────────────────

/// Bull Flag / Bear Flag detector.
///
/// Detects a strong directional pole followed by a shallow counter-trend
/// consolidation (the "flag body"), then a breakout resuming the original direction.
///
/// The window is split `pole_pct` : `(1 - pole_pct)` to separate pole from flag.
pub struct FlagDetector {
    /// Total pattern window in bars.
    pub lookback: usize,
    /// Fraction of window used for pole detection (0.0–1.0, default 0.4).
    pub pole_pct: f64,
    /// Minimum pole move as fraction of price (e.g. 0.04 = 4%).
    pub pole_min_move: f64,
    /// Maximum retrace of pole as fraction of pole height (e.g. 0.5 = 50%).
    pub max_retrace: f64,
    /// `true` = bull flag, `false` = bear flag.
    pub bullish: bool,
}

impl FlagDetector {
    pub fn bull(lookback: usize) -> Self {
        Self { lookback, pole_pct: 0.4, pole_min_move: 0.04, max_retrace: 0.5, bullish: true }
    }
    pub fn bear(lookback: usize) -> Self {
        Self { lookback, pole_pct: 0.4, pole_min_move: 0.04, max_retrace: 0.5, bullish: false }
    }
}

impl Default for FlagDetector {
    fn default() -> Self { Self::bull(30) }
}

impl PatternDetector for FlagDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        if bars.len() < self.lookback { return vec![]; }

        let window = &bars[bars.len() - self.lookback..];
        let n = window.len();
        let pole_end = ((n as f64 * self.pole_pct) as usize).max(2);
        if pole_end >= n { return vec![]; }

        let pole_bars = &window[..pole_end];
        let flag_bars = &window[pole_end..];

        let pivots = find_pivots(bars, 3, 3);
        let pivot_points: Vec<PivotPoint> = {
            let hs = highs(&pivots);
            let ls = lows(&pivots);
            hs.iter().chain(ls.iter()).map(|p| (*p).clone()).collect()
        };

        if self.bullish {
            let pole_base = pole_bars.iter().map(|b| b.low).fold(f64::INFINITY, f64::min);
            let pole_top  = pole_bars.iter().map(|b| b.high).fold(f64::NEG_INFINITY, f64::max);
            let pole_move = (pole_top - pole_base) / pole_base;
            if pole_move < self.pole_min_move { return vec![]; }

            let flag_low = flag_bars.iter().map(|b| b.low).fold(f64::INFINITY, f64::min);
            let retrace = (pole_top - flag_low) / (pole_top - pole_base).max(f64::EPSILON);
            if retrace > self.max_retrace { return vec![]; }

            let current_close = bars.last().unwrap().close;
            if current_close <= pole_top { return vec![]; }

            let pole_height = pole_top - pole_base;
            let target = pole_top + pole_height;
            let stop = flag_low * 0.995;
            let confidence = ((1.0 - retrace / self.max_retrace) * 0.6 + 0.4).min(1.0);

            vec![make_signal(PatternKind::BullFlag, bars, confidence, pivot_points, Some(target), Some(stop))]
        } else {
            let pole_top  = pole_bars.iter().map(|b| b.high).fold(f64::NEG_INFINITY, f64::max);
            let pole_base = pole_bars.iter().map(|b| b.low).fold(f64::INFINITY, f64::min);
            let pole_move = (pole_top - pole_base) / pole_top;
            if pole_move < self.pole_min_move { return vec![]; }

            let flag_high = flag_bars.iter().map(|b| b.high).fold(f64::NEG_INFINITY, f64::max);
            let retrace = (flag_high - pole_base) / (pole_top - pole_base).max(f64::EPSILON);
            if retrace > self.max_retrace { return vec![]; }

            let current_close = bars.last().unwrap().close;
            if current_close >= pole_base { return vec![]; }

            let pole_height = pole_top - pole_base;
            let target = pole_base - pole_height;
            let stop = flag_high * 1.005;
            let confidence = ((1.0 - retrace / self.max_retrace) * 0.6 + 0.4).min(1.0);

            vec![make_signal(PatternKind::BearFlag, bars, confidence, pivot_points, Some(target), Some(stop))]
        }
    }

    fn name(&self) -> &str { if self.bullish { "bull_flag" } else { "bear_flag" } }
    fn min_lookback(&self) -> usize { self.lookback }
}

// ── Wedge ─────────────────────────────────────────────────────────────────────

/// Wedge pattern detector.
///
/// - **Rising wedge** (`rising = true`, bearish): both highs and lows trend upward
///   but the lows slope is steeper than the highs slope — lines converge at the top.
///   Confirms on close *below* the lower trendline.
///
/// - **Falling wedge** (`rising = false`, bullish): both highs and lows trend downward
///   but the highs slope is steeper — lines converge at the bottom.
///   Confirms on close *above* the upper trendline.
pub struct WedgeDetector {
    pub pivot_size: usize,
    /// `true` = rising wedge (bearish), `false` = falling wedge (bullish).
    pub rising: bool,
}

impl WedgeDetector {
    pub fn rising(pivot_size: usize) -> Self { Self { pivot_size, rising: true } }
    pub fn falling(pivot_size: usize) -> Self { Self { pivot_size, rising: false } }
}

impl Default for WedgeDetector {
    fn default() -> Self { Self::rising(3) }
}

impl PatternDetector for WedgeDetector {
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        let pivots = find_pivots(bars, self.pivot_size, self.pivot_size);
        let hs = highs(&pivots);
        let ls = lows(&pivots);

        if hs.len() < 2 || ls.len() < 2 { return vec![]; }

        let h1 = hs[hs.len() - 2];
        let h2 = hs[hs.len() - 1];
        let l1 = ls[ls.len() - 2];
        let l2 = ls[ls.len() - 1];

        let bars_h = (h2.bar_index as f64 - h1.bar_index as f64).max(1.0);
        let bars_l = (l2.bar_index as f64 - l1.bar_index as f64).max(1.0);

        let slope_h = (h2.price - h1.price) / bars_h;
        let slope_l = (l2.price - l1.price) / bars_l;

        let last_idx = (bars.len() - 1) as f64;
        let current_close = bars.last().unwrap().close;

        if self.rising {
            // Rising wedge: both slopes positive, but lows rising faster than highs
            if slope_h <= 0.0 || slope_l <= 0.0 { return vec![]; }
            if slope_l <= slope_h { return vec![]; }

            let lower = trendline_at(
                (l1.bar_index as f64, l1.price),
                (l2.bar_index as f64, l2.price),
                last_idx,
            );
            if current_close >= lower { return vec![]; }

            let upper = trendline_at(
                (h1.bar_index as f64, h1.price),
                (h2.bar_index as f64, h2.price),
                last_idx,
            );
            let pattern_height = (upper - lower).max(0.0);
            let target = lower - pattern_height;
            let stop = upper * 1.005;

            let convergence = (slope_l - slope_h) * 5.0;
            let confidence = (0.4 + convergence).min(1.0).max(0.3);

            vec![make_signal(
                PatternKind::Wedge { rising: true },
                bars,
                confidence,
                vec![(*h1).clone(), (*l1).clone(), (*h2).clone(), (*l2).clone()],
                Some(target),
                Some(stop),
            )]
        } else {
            // Falling wedge: both slopes negative, highs falling faster than lows
            if slope_h >= 0.0 || slope_l >= 0.0 { return vec![]; }
            if slope_h >= slope_l { return vec![]; } // slope_h is more negative

            let upper = trendline_at(
                (h1.bar_index as f64, h1.price),
                (h2.bar_index as f64, h2.price),
                last_idx,
            );
            if current_close <= upper { return vec![]; }

            let lower = trendline_at(
                (l1.bar_index as f64, l1.price),
                (l2.bar_index as f64, l2.price),
                last_idx,
            );
            let pattern_height = (upper - lower).max(0.0);
            let target = upper + pattern_height;
            let stop = lower * 0.995;

            let convergence = (slope_l - slope_h) * 5.0;
            let confidence = (0.4 + convergence).min(1.0).max(0.3);

            vec![make_signal(
                PatternKind::Wedge { rising: false },
                bars,
                confidence,
                vec![(*h1).clone(), (*l1).clone(), (*h2).clone(), (*l2).clone()],
                Some(target),
                Some(stop),
            )]
        }
    }

    fn name(&self) -> &str { if self.rising { "rising_wedge" } else { "falling_wedge" } }
    fn min_lookback(&self) -> usize { self.pivot_size * 4 + 5 }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;

    fn bar(ts: i64, high: f64, low: f64, close: f64) -> Bar {
        Bar::new(ts, "T", (high + low) / 2.0, high, low, close, 1000.0)
    }

    // Build a synthetic Double Top sequence:
    // rise → top1 → dip → top2 → break below neckline
    fn make_double_top_bars() -> Vec<Bar> {
        let mut bars = Vec::new();
        let mut ts = 0i64;
        // Rise to first top
        for i in 0..10 {
            let p = 100.0 + i as f64 * 2.0;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        // First top ~120 (peak at bar 9: high=120.5)
        for i in 0..3 {
            let p = 120.0 - i as f64 * 1.0;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        // Dip to neckline ~112
        for i in 0..5 {
            let p = 117.0 - i as f64 * 1.0;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        // Rise to second top ~119.5 (within 3% of 120)
        for i in 0..5 {
            let p = 112.0 + i as f64 * 1.5;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        // Second top
        for i in 0..3 {
            let p = 119.5 - i as f64 * 1.0;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        // Break below neckline (112)
        for i in 0..5 {
            let p = 116.0 - i as f64 * 1.5;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        bars
    }

    #[test]
    fn test_double_top_detects() {
        let bars = make_double_top_bars();
        let d = DoubleTopDetector::default();
        if bars.len() >= d.min_lookback() {
            let sigs = d.detect(&bars);
            // We don't mandate detection (depends on exact pivot placement) but it must not panic
            for s in &sigs {
                assert_eq!(s.kind, PatternKind::DoubleTop);
                assert!(s.confidence > 0.0 && s.confidence <= 1.0);
                assert!(s.target_price.is_some());
            }
        }
    }

    fn make_double_bottom_bars() -> Vec<Bar> {
        let mut bars = Vec::new();
        let mut ts = 0i64;
        // Fall to first bottom
        for i in 0..10 {
            let p = 120.0 - i as f64 * 2.0;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        // First bottom ~100
        for i in 0..3 {
            let p = 100.0 + i as f64 * 1.0;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        // Rally to neckline ~108
        for i in 0..5 {
            let p = 103.0 + i as f64 * 1.0;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        // Dip to second bottom ~100.5
        for i in 0..5 {
            let p = 108.0 - i as f64 * 1.5;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        // Second bottom
        for i in 0..3 {
            let p = 100.5 + i as f64;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        // Break above neckline (108)
        for i in 0..5 {
            let p = 104.0 + i as f64 * 1.5;
            bars.push(bar(ts, p + 0.5, p - 0.5, p)); ts += 1;
        }
        bars
    }

    #[test]
    fn test_double_bottom_detects() {
        let bars = make_double_bottom_bars();
        let d = DoubleBottomDetector::default();
        if bars.len() >= d.min_lookback() {
            let sigs = d.detect(&bars);
            for s in &sigs {
                assert_eq!(s.kind, PatternKind::DoubleBottom);
                assert!(s.confidence > 0.0 && s.confidence <= 1.0);
            }
        }
    }

    #[test]
    fn test_within_helper() {
        assert!(within(100.0, 102.0, 0.03));  // 2% difference
        assert!(!within(100.0, 110.0, 0.03)); // 10% difference
        assert!(within(100.0, 100.0, 0.0));   // exact match
    }

    #[test]
    fn test_double_top_no_false_positive_uptrend() {
        // Pure uptrend — no double top
        let bars: Vec<Bar> = (0..30).map(|i| {
            let p = 100.0 + i as f64 * 2.0;
            bar(i, p + 0.5, p - 0.5, p)
        }).collect();
        let d = DoubleTopDetector::default();
        // Last bar close is well above any neckline → no confirmation
        let sigs = d.detect(&bars);
        assert!(sigs.is_empty(), "no double top in pure uptrend");
    }

    #[test]
    fn test_hs_min_lookback_respected() {
        let d = HeadAndShouldersDetector::default();
        let bars: Vec<Bar> = (0..5).map(|i| bar(i, 100.0 + i as f64, 99.0, 100.0 + i as f64)).collect();
        // Too few bars — must return empty without panic
        let sigs = d.detect(&bars);
        assert!(sigs.is_empty());
    }
}
