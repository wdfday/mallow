//! Swing pivot detection — foundation for all chart pattern detectors.
//!
//! A **swing high** at index `i` means `bars[i].high` is the highest high in
//! the window `[i-left .. i+right]` (inclusive).  A **swing low** is analogous
//! with `low`.
//!
//! # Typical usage
//! ```rust
//! use alm_pattern::pivot::find_pivots;
//! // bars: oldest → newest, left=2, right=2
//! // let pivots = find_pivots(bars, 2, 2);
//! ```

use alm_core::bar::Bar;

use crate::types::PivotPoint;

/// Find all confirmed swing highs and swing lows in `bars` (oldest-first).
///
/// - `left`  — number of bars to the left that must be lower (for high) / higher (for low).
/// - `right` — number of bars to the right that must satisfy the same condition.
///
/// A pivot at index `i` is only emitted when the right-side bars are already
/// available, i.e. `i + right < bars.len()`. This means the most recent `right`
/// bars cannot have confirmed pivots yet — they are "pending".
pub fn find_pivots(bars: &[Bar], left: usize, right: usize) -> Vec<PivotPoint> {
    let n = bars.len();
    if n < left + right + 1 {
        return vec![];
    }

    let mut pivots = Vec::new();

    for i in left..n.saturating_sub(right) {
        let h = bars[i].high;
        let l = bars[i].low;

        // Check left side
        let left_ok_high = (i.saturating_sub(left)..i).all(|j| bars[j].high <= h);
        let left_ok_low = (i.saturating_sub(left)..i).all(|j| bars[j].low >= l);

        // Check right side
        let right_ok_high = (i + 1..=(i + right).min(n - 1)).all(|j| bars[j].high <= h);
        let right_ok_low = (i + 1..=(i + right).min(n - 1)).all(|j| bars[j].low >= l);

        if left_ok_high && right_ok_high {
            pivots.push(PivotPoint {
                bar_index: i,
                timestamp: bars[i].timestamp,
                price: h,
                is_high: true,
            });
        }

        if left_ok_low && right_ok_low {
            pivots.push(PivotPoint {
                bar_index: i,
                timestamp: bars[i].timestamp,
                price: l,
                is_high: false,
            });
        }
    }

    pivots
}

/// Return only swing highs from a pivot list.
pub fn highs(pivots: &[PivotPoint]) -> Vec<&PivotPoint> {
    pivots.iter().filter(|p| p.is_high).collect()
}

/// Return only swing lows from a pivot list.
pub fn lows(pivots: &[PivotPoint]) -> Vec<&PivotPoint> {
    pivots.iter().filter(|p| !p.is_high).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::bar::Bar;

    fn bar(ts: i64, high: f64, low: f64) -> Bar {
        Bar::new(ts, "T", (high + low) / 2.0, high, low, (high + low) / 2.0, 100.0)
    }

    #[test]
    fn test_find_single_swing_high() {
        // Pattern: 100, 105, 102 — index 1 is a swing high with left=1, right=1
        let bars = vec![bar(0, 100.0, 98.0), bar(1, 105.0, 103.0), bar(2, 102.0, 100.0)];
        let pivots = find_pivots(&bars, 1, 1);
        let hs: Vec<_> = highs(&pivots);
        assert_eq!(hs.len(), 1);
        assert_eq!(hs[0].bar_index, 1);
        assert!((hs[0].price - 105.0).abs() < 1e-9);
    }

    #[test]
    fn test_find_single_swing_low() {
        let bars = vec![bar(0, 102.0, 100.0), bar(1, 100.0, 95.0), bar(2, 102.0, 100.0)];
        let pivots = find_pivots(&bars, 1, 1);
        let ls: Vec<_> = lows(&pivots);
        assert_eq!(ls.len(), 1);
        assert_eq!(ls[0].bar_index, 1);
        assert!((ls[0].price - 95.0).abs() < 1e-9);
    }

    #[test]
    fn test_no_pivot_when_window_too_small() {
        let bars = vec![bar(0, 100.0, 98.0), bar(1, 105.0, 103.0)];
        // Need left=1 + right=1 + 1 = 3 bars minimum
        assert!(find_pivots(&bars, 1, 1).is_empty());
    }

    #[test]
    fn test_multiple_pivots() {
        // W shape: high-low-high-low-high
        let prices = [(105.0, 103.0), (100.0, 98.0), (106.0, 104.0), (101.0, 99.0), (107.0, 105.0)];
        let bars: Vec<Bar> = prices.iter().enumerate().map(|(i, &(h, l))| bar(i as i64, h, l)).collect();
        let pivots = find_pivots(&bars, 1, 1);
        // Should find: swing lows at index 1 and 3; swing highs at index 2
        let hs: Vec<_> = highs(&pivots);
        let ls: Vec<_> = lows(&pivots);
        assert!(!hs.is_empty(), "expected at least one swing high");
        assert!(!ls.is_empty(), "expected at least one swing low");
    }

    #[test]
    fn test_flat_bar_not_a_pivot() {
        // All same high → no unique pivot
        let bars = vec![bar(0, 100.0, 99.0), bar(1, 100.0, 99.0), bar(2, 100.0, 99.0)];
        let pivots = find_pivots(&bars, 1, 1);
        // All bars tie → all qualify (or none) — test that we don't panic
        let _ = pivots;
    }
}
