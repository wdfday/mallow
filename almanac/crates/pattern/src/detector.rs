use alm_core::bar::Bar;

use crate::types::PatternSignal;

/// Core interface for geometric pattern detectors.
///
/// Detectors are **stateless across calls** — each call to `detect()` receives
/// the full window slice and performs its scan from scratch. This makes them
/// easy to compose and test without needing `reset()`.
///
/// # Contract
/// - `bars` is ordered **oldest-first** (index 0 = oldest).
/// - `detect()` is only called when `bars.len() >= min_lookback()`.
/// - Implementations must be `Send` so they can run inside the engine thread.
pub trait PatternDetector: Send {
    /// Scan the window for confirmed patterns.
    fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal>;

    /// Detector's identifier (e.g. `"double_top"`).
    fn name(&self) -> &str;

    /// Minimum number of bars the detector needs before it can produce output.
    /// The engine will skip calling `detect()` until the window is this large.
    fn min_lookback(&self) -> usize;
}

// ── Composite ────────────────────────────────────────────────────────────────

/// Runs multiple detectors over the same bar window and merges all results.
///
/// ```rust
/// use alm_pattern::detector::CompositeDetector;
/// let composite = CompositeDetector::new(vec![]); // empty — no-op
/// assert!(composite.detect(&[]).is_empty());
/// ```
pub struct CompositeDetector {
    detectors: Vec<Box<dyn PatternDetector>>,
}

impl CompositeDetector {
    pub fn new(detectors: Vec<Box<dyn PatternDetector>>) -> Self {
        Self { detectors }
    }

    /// Run all detectors and collect all pattern signals.
    pub fn detect(&self, bars: &[Bar]) -> Vec<PatternSignal> {
        self.detectors
            .iter()
            .filter(|d| bars.len() >= d.min_lookback())
            .flat_map(|d| d.detect(bars))
            .collect()
    }

    /// The largest `min_lookback` across all detectors — the engine should
    /// buffer at least this many bars before calling `detect()`.
    pub fn max_lookback(&self) -> usize {
        self.detectors
            .iter()
            .map(|d| d.min_lookback())
            .max()
            .unwrap_or(0)
    }

    /// Number of registered detectors.
    pub fn len(&self) -> usize {
        self.detectors.len()
    }

    pub fn is_empty(&self) -> bool {
        self.detectors.is_empty()
    }
}

// ── Stub / no-op ─────────────────────────────────────────────────────────────

/// A detector that never fires — useful as a placeholder or in tests.
///
/// ```rust
/// use alm_pattern::detector::{NoopDetector, PatternDetector};
/// let d = NoopDetector;
/// assert!(d.detect(&[]).is_empty());
/// assert_eq!(d.name(), "noop");
/// ```
pub struct NoopDetector;

impl PatternDetector for NoopDetector {
    fn detect(&self, _bars: &[Bar]) -> Vec<PatternSignal> {
        vec![]
    }
    fn name(&self) -> &str {
        "noop"
    }
    fn min_lookback(&self) -> usize {
        1
    }
}
