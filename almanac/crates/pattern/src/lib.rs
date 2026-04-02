//! `alm-pattern` — geometric and candlestick pattern interfaces.
//!
//! This crate provides:
//! - [`PatternKind`] — enum of every supported pattern type
//! - [`PatternSignal`] — output emitted when a pattern is confirmed (includes
//!   confidence, entry/target/stop prices, and pivot points for `alm-chart`)
//! - [`PatternDetector`] trait — implement this to add a new detector
//! - [`CompositeDetector`] — fan-out multiple detectors over the same window
//! - [`NoopDetector`] — stub for testing or as a placeholder
//!
//! # How it fits in the engine
//!
//! The backtesting engine maintains a sliding window of recent [`alm_core::bar::Bar`]s.
//! After each `on_bar()` call it also calls [`alm_core::strategy::Strategy::on_window()`]
//! with that window. Strategies that embed a [`CompositeDetector`] can call
//! `detector.detect(bars)` inside `on_window()` and convert the resulting
//! [`PatternSignal`]s into [`alm_core::signal::Signal`]s.
//!
//! # Example skeleton
//!
//! ```rust,ignore
//! use alm_pattern::detector::{CompositeDetector, NoopDetector};
//!
//! let detector = CompositeDetector::new(vec![
//!     Box::new(NoopDetector), // replace with real detectors
//! ]);
//! ```

pub mod candlestick;
pub mod chart;
pub mod detector;
pub mod pivot;
pub mod types;

pub use candlestick::{
    DojiDetector, EngulfingDetector, EveningStarDetector, HammerDetector, MarubozuDetector,
    MorningStarDetector, ShootingStarDetector, ThreeBlackCrowsDetector, ThreeWhiteSoldiersDetector,
};
pub use chart::{
    AscendingTriangleDetector, DescendingTriangleDetector, DoubleBottomDetector,
    DoubleTopDetector, FlagDetector, HeadAndShouldersDetector, InverseHeadAndShouldersDetector,
    SymmetricTriangleDetector, WedgeDetector,
};
pub use detector::{CompositeDetector, NoopDetector, PatternDetector};
pub use pivot::find_pivots;
pub use types::{PatternKind, PatternSignal, PivotPoint};
