//! Pattern-based breakout strategy.
//!
//! Runs a [`alm_pattern::detector::CompositeDetector`] inside the `on_window()` hook
//! each bar, converting confirmed [`alm_pattern::PatternSignal`]s into engine
//! [`alm_core::signal::Signal`]s.
//!
//! ## Signal mapping
//! | Pattern | Signal |
//! |---------|--------|
//! | DoubleBottom, InverseH&S, AscendingTriangle, BullFlag, FallingWedge | `Long` |
//! | DoubleTop, H&S, DescendingTriangle, BearFlag, RisingWedge           | `Short` (closes long) |
//!
//! Entry is only taken when `pattern.confidence >= min_confidence`.
//! A bearish pattern while flat is a no-op (short-selling not modelled here).

use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};
use alm_pattern::{
    detector::CompositeDetector,
    types::PatternKind,
    AscendingTriangleDetector, DescendingTriangleDetector, DoubleBottomDetector,
    DoubleTopDetector, FlagDetector, HeadAndShouldersDetector, InverseHeadAndShouldersDetector,
    SymmetricTriangleDetector, WedgeDetector,
};

// ── Pattern direction helpers ─────────────────────────────────────────────────

fn is_bullish(kind: &PatternKind) -> bool {
    matches!(
        kind,
        PatternKind::DoubleBottom
            | PatternKind::InverseHeadAndShoulders
            | PatternKind::AscendingTriangle
            | PatternKind::BullFlag
            | PatternKind::BullPennant
            | PatternKind::Wedge { rising: false }
    )
}

fn is_bearish(kind: &PatternKind) -> bool {
    matches!(
        kind,
        PatternKind::DoubleTop
            | PatternKind::HeadAndShoulders
            | PatternKind::DescendingTriangle
            | PatternKind::BearFlag
            | PatternKind::BearPennant
            | PatternKind::Wedge { rising: true }
    )
}

// ── PatternBreakoutStrategy ───────────────────────────────────────────────────

/// Breakout strategy driven by geometric chart patterns.
///
/// Pairs with the `alm-pattern` crate's `CompositeDetector`.  Build it with
/// [`PatternBreakoutStrategy::default_setup`] for a ready-to-use configuration
/// covering reversal + continuation patterns.
pub struct PatternBreakoutStrategy {
    detector: CompositeDetector,
    pub window_size: usize,
    min_confidence: f64,
}

impl PatternBreakoutStrategy {
    pub fn new(
        detector: CompositeDetector,
        window_size: usize,
        min_confidence: f64,
    ) -> Self {
        Self { detector, window_size, min_confidence}
    }

    /// All-patterns preset: 4 reversal + 5 continuation detectors, 60% confidence gate.
    ///
    /// `window_size` controls the sliding-window length passed to `on_window()`
    /// (default 60 bars gives enough history for most patterns on daily data).
    pub fn default_setup(window_size: usize) -> Self {
        let detector = CompositeDetector::new(vec![
            // ── Reversal ──────────────────────────────────────────────────────
            Box::new(DoubleBottomDetector::default()),
            Box::new(DoubleTopDetector::default()),
            Box::new(InverseHeadAndShouldersDetector::default()),
            Box::new(HeadAndShouldersDetector::default()),
            // ── Continuation ─────────────────────────────────────────────────
            Box::new(AscendingTriangleDetector::default()),
            Box::new(DescendingTriangleDetector::default()),
            Box::new(SymmetricTriangleDetector::default()),
            Box::new(FlagDetector::bull(window_size)),
            Box::new(WedgeDetector::falling(3)),
        ]);
        Self::new(detector, window_size, 0.6)
    }
}

impl Strategy for PatternBreakoutStrategy {
    /// `on_bar` is a no-op — all logic lives in `on_window`.
    fn on_bar(&mut self, _bar: &Bar) -> Vec<Signal> {
        vec![]
    }

    fn on_window(&mut self, bars: &[Bar]) -> Vec<Signal> {
        if bars.len() < 20 { return vec![]; }

        let pattern_signals = self.detector.detect(bars);
        if pattern_signals.is_empty() { return vec![]; }

        let last = bars.last().unwrap();
        let ts = last.timestamp;
        let symbol = &last.symbol;

        // Pick the highest-confidence signal that clears the threshold
        let best = pattern_signals
            .iter()
            .filter(|s| s.confidence >= self.min_confidence)
            .max_by(|a, b| a.confidence.partial_cmp(&b.confidence).unwrap());

        let Some(sig) = best else { return vec![]; };

        if is_bullish(&sig.kind) {
            return vec![Signal::long(ts, symbol, sig.confidence)];
        }

        if is_bearish(&sig.kind) {
            return vec![Signal::close(ts, symbol)];
        }

        vec![]
    }

    fn uses_window(&self) -> bool { true }
    fn name(&self) -> &str { "pattern_breakout" }

    fn reset(&mut self) {
        
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;

    fn bar(ts: i64, close: f64) -> Bar {
        Bar::new(ts, "T", close, close * 1.005, close * 0.995, close, 1000.0)
    }

    #[test]
    fn test_no_signals_on_empty_window() {
        let mut s = PatternBreakoutStrategy::default_setup(60);
        let bars: Vec<Bar> = (0..5).map(|i| bar(i, 100.0)).collect();
        let sigs = s.on_window(&bars);
        assert!(sigs.is_empty());
    }

    #[test]
    fn test_on_bar_always_empty() {
        let mut s = PatternBreakoutStrategy::default_setup(60);
        let b = bar(0, 100.0);
        assert!(s.on_bar(&b).is_empty());
    }
}
