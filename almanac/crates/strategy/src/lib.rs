// ── Factory ───────────────────────────────────────────────────────────────────
pub mod factory;
pub use factory::build_strategy;

// ── Candle transform ──────────────────────────────────────────────────────────
pub mod candle_type;
pub use candle_type::{CandleTransform, CandleType};

// ── Expression / template strategies ─────────────────────────────────────────
pub mod expr;
pub use expr::{CelScript, CelStrategy};

// ── Declarative JSON strategy ─────────────────────────────────────────────────
pub mod dynamic;
pub use dynamic::DynamicStrategy;

// ── Layered (filter + signal) strategy ───────────────────────────────────────
pub mod layered;
pub use layered::LayeredStrategy;

// ── Bar resampler (MTF helper) ────────────────────────────────────────────────
pub mod bar_resampler;

// ── Concrete named strategies (58) ───────────────────────────────────────────
pub mod named;
pub use named::*;

// ── Shared test utilities ─────────────────────────────────────────────────────
#[cfg(test)]
pub mod test_utils;

// ── Tests ─────────────────────────────────────────────────────────────────────
#[cfg(test)]
mod edge_case_tests;
