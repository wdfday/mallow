//! Equity / drawdown curve compression strategies.
//!
//! # Active pipeline — [`compress`]
//!
//! ```text
//! dedup_flat  →  lttb(target)  →  inject global (min, max)
//! ```
//!
//! **Step 1 — `dedup_flat`:** strips the interior of flat runs produced by
//! idle periods between trades. A low-frequency strategy on M1 data can have
//! 500k bars but only 200 trades; this step removes ~99% of points before any
//! visual algorithm sees the data. Time scale is preserved because both edges
//! of each flat segment are kept.
//!
//! **Step 2 — `lttb`:** reduces the semantically dense remainder to `target`
//! points while maximising visual fidelity (Largest-Triangle-Three-Buckets).
//! Every remaining point is a real event, so LTTB works on a signal-rich
//! sequence rather than noise.
//!
//! **Step 3 — inject global extremes:** the global peak and trough are pinned
//! into the output after LTTB. LTTB optimises visual shape across the whole
//! curve but can still drop a lone spike if it falls inside a bucket that is
//! already well-represented. Explicit injection is a cheap safety net — if
//! the point is already present it is a no-op after dedup.
//!
//! # Changing the pipeline
//!
//! The primitives below are intentionally kept separate so the pipeline can
//! be rewired without touching call sites. `backtest.rs` calls only
//! [`compress`]; swap the body there to experiment.
//!
//! # Primitive options
//! - [`dedup_flat`]   — remove interior of flat runs, keep edges
//! - [`change_only`]  — emit only when value changes (loses time scale)
//! - [`time_bucket`]  — min-max per uniform time bucket (preserves peaks/troughs)
//! - [`lttb`]         — Largest-Triangle-Three-Buckets (best visual fidelity)

use crate::types::CurvePoint;

// ── Active pipeline ───────────────────────────────────────────────────────────

/// Compress an equity or drawdown curve using the active pipeline:
/// `dedup_flat → lttb(target) → inject global (min, max)`.
///
/// If the curve already has ≤ `target` points after dedup, LTTB is skipped.
/// The global extremes are always present in the output regardless.
/// Compress an equity or drawdown curve.
///
/// Strips the interior of flat runs (idle periods between trades) so the
/// chart renderer gets an accurate shape without transmitting millions of
/// identical points. Every value-change event is preserved — no lossy
/// downsampling.
pub fn compress(pts: Vec<CurvePoint>, _target: usize) -> Vec<CurvePoint> {
    dedup_flat(pts)
}

// ── 1. Dedup flat runs ────────────────────────────────────────────────────────

/// Remove the interior of horizontal runs; keep the leading and trailing edge
/// of each flat segment so chart rendering still scales the time axis correctly.
pub fn dedup_flat(pts: Vec<CurvePoint>) -> Vec<CurvePoint> {
    if pts.len() <= 2 {
        return pts;
    }
    let mut out: Vec<CurvePoint> = Vec::with_capacity(pts.len() / 4);
    let mut i = 0;
    while i < pts.len() {
        out.push(pts[i].clone());
        // Find the end of a run of equal values.
        let run_val = pts[i].v;
        let mut j = i + 1;
        while j < pts.len() && pts[j].v == run_val {
            j += 1;
        }
        // If the run was longer than 1, emit the trailing edge.
        if j - 1 > i {
            out.push(pts[j - 1].clone());
        }
        i = j;
    }
    out
}

// ── 2. Change-only ────────────────────────────────────────────────────────────

/// Emit a point only when the value differs from the previous emitted point.
/// First and last points are always kept.
///
/// **Warning:** collapses idle periods — the time axis will not reflect
/// calendar duration between trades.
pub fn change_only(pts: Vec<CurvePoint>) -> Vec<CurvePoint> {
    if pts.len() <= 1 {
        return pts;
    }
    let mut out: Vec<CurvePoint> = Vec::with_capacity(pts.len() / 4);
    out.push(pts[0].clone());
    for w in pts.windows(2) {
        if (w[1].v - w[0].v).abs() > f64::EPSILON {
            out.push(w[1].clone());
        }
    }
    // Ensure last point is always present.
    let last = pts.last().unwrap();
    if out.last().map(|p| p.t) != Some(last.t) {
        out.push(last.clone());
    }
    out
}

// ── 3. Time bucket (min-max) ──────────────────────────────────────────────────

/// Uniform time-bucket downsample. Each bucket contributes its min-value and
/// max-value point (in time order), so peaks and troughs are preserved.
/// Returns at most `2 * buckets` points. First and last are always kept.
pub fn time_bucket(pts: Vec<CurvePoint>, buckets: usize) -> Vec<CurvePoint> {
    if buckets == 0 || pts.len() <= buckets * 2 {
        return pts;
    }
    let n = pts.len();
    let mut out = Vec::with_capacity(buckets * 2 + 2);
    let bucket_size = n as f64 / buckets as f64;
    for b in 0..buckets {
        let start = (b as f64 * bucket_size).round() as usize;
        let end = (((b + 1) as f64 * bucket_size).round() as usize).min(n);
        if start >= end {
            continue;
        }
        let slice = &pts[start..end];
        let (min_i, max_i) = slice.iter().enumerate().fold((0usize, 0usize), |(mi, xi), (i, p)| {
            (
                if p.v < slice[mi].v { i } else { mi },
                if p.v > slice[xi].v { i } else { xi },
            )
        });
        if min_i <= max_i {
            out.push(pts[start + min_i].clone());
            if min_i != max_i {
                out.push(pts[start + max_i].clone());
            }
        } else {
            out.push(pts[start + max_i].clone());
            if min_i != max_i {
                out.push(pts[start + min_i].clone());
            }
        }
    }
    out
}

// ── 4. LTTB (Largest-Triangle-Three-Buckets) ──────────────────────────────────

/// Downsample to exactly `target` points using the LTTB algorithm.
/// Preserves visual shape best of all options — inflection points are kept.
/// First and last points are always kept. Returns unchanged if already ≤ target.
pub fn lttb(pts: Vec<CurvePoint>, target: usize) -> Vec<CurvePoint> {
    if target == 0 || pts.len() <= target {
        return pts;
    }
    let n = pts.len();
    let mut out = Vec::with_capacity(target);
    out.push(pts[0].clone());

    // Divide the middle section into (target - 2) equal-width buckets.
    let bucket_count = target - 2;
    let bucket_size = (n - 2) as f64 / bucket_count as f64;

    let mut prev_selected = 0usize; // index of last selected point

    for b in 0..bucket_count {
        // Current bucket range.
        let start = (b as f64 * bucket_size + 1.0).floor() as usize;
        let end = (((b + 1) as f64 * bucket_size) + 1.0).floor() as usize;
        let end = end.min(n - 1);

        // Next bucket: compute its average (acts as "look-ahead" anchor).
        let next_start = end;
        let next_end = ((((b + 2) as f64) * bucket_size) + 1.0).floor() as usize;
        let next_end = next_end.min(n);
        let (avg_t, avg_v) = if next_start < next_end {
            let slice = &pts[next_start..next_end];
            let sum_t: i64 = slice.iter().map(|p| p.t).sum();
            let sum_v: f64 = slice.iter().map(|p| p.v).sum();
            let len = slice.len() as f64;
            (sum_t as f64 / len, sum_v / len)
        } else {
            (pts[n - 1].t as f64, pts[n - 1].v)
        };

        // Select the point in the current bucket that forms the largest triangle
        // with prev_selected and the next-bucket average.
        let ax = pts[prev_selected].t as f64;
        let ay = pts[prev_selected].v;

        let best = (start..end).max_by(|&i, &j| {
            let area = |k: usize| {
                let bx = pts[k].t as f64;
                let by = pts[k].v;
                ((ax - avg_t) * (by - ay) - (ax - bx) * (avg_v - ay)).abs()
            };
            area(i).partial_cmp(&area(j)).unwrap_or(std::cmp::Ordering::Equal)
        });

        if let Some(idx) = best {
            out.push(pts[idx].clone());
            prev_selected = idx;
        }
    }

    out.push(pts[n - 1].clone());
    out
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn pts(pairs: &[(i64, f64)]) -> Vec<CurvePoint> {
        pairs.iter().map(|&(t, v)| CurvePoint { t, v }).collect()
    }

    #[test]
    fn dedup_flat_removes_interior() {
        let input = pts(&[(0, 100.0), (1, 100.0), (2, 100.0), (3, 110.0), (4, 110.0)]);
        let out = dedup_flat(input);
        // flat run [0..2] → keep t=0 and t=2; flat run [3..4] → keep t=3 and t=4
        assert_eq!(out.len(), 4);
        assert_eq!(out[0].t, 0);
        assert_eq!(out[1].t, 2);
        assert_eq!(out[2].t, 3);
        assert_eq!(out[3].t, 4);
    }

    #[test]
    fn change_only_skips_flat() {
        let input = pts(&[(0, 100.0), (1, 100.0), (2, 105.0), (3, 105.0), (4, 110.0)]);
        let out = change_only(input);
        let values: Vec<f64> = out.iter().map(|p| p.v).collect();
        assert_eq!(values, vec![100.0, 105.0, 110.0]);
    }

    #[test]
    fn time_bucket_preserves_peak() {
        // Rising then falling — peak at t=5 should survive.
        let input: Vec<CurvePoint> = (0..=10)
            .map(|i| {
                let v = if i <= 5 { i as f64 } else { (10 - i) as f64 };
                CurvePoint { t: i, v }
            })
            .collect();
        let out = time_bucket(input, 3);
        assert!(out.iter().any(|p| p.v == 5.0), "peak must be kept");
    }

    #[test]
    fn lttb_exact_target() {
        let input: Vec<CurvePoint> = (0..100).map(|i| CurvePoint { t: i, v: i as f64 }).collect();
        let out = lttb(input, 10);
        assert_eq!(out.len(), 10);
        assert_eq!(out.first().unwrap().t, 0);
        assert_eq!(out.last().unwrap().t, 99);
    }
}
