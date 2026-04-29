// ── Factory ───────────────────────────────────────────────────────────────────
pub mod factory;
pub use factory::build_strategy;

// ── Candle transform ──────────────────────────────────────────────────────────
pub mod candle_type;
pub use candle_type::{CandleTransform, CandleType};

// ── Expression / template strategies ─────────────────────────────────────────
pub mod expr;
pub use expr::{CelScript, CelStrategy, RhaiStrategy};

// // ── Declarative JSON strategy ─────────────────────────────────────────────────
// pub mod dynamic;  // deprecated — use CelStrategy instead
// pub use dynamic::DynamicStrategy;

// ── Bar resampler (MTF helper) ────────────────────────────────────────────────
pub mod bar_resampler;

// ── Strategy / indicator catalogue ────────────────────────────────────────────
// Static metadata surfaced by HTTP (`GET /api/indicators`, `/api/strategies`)
// and used by docs / UI pickers. Lives in strategy because the source of
// truth for what strategies + indicators exist is this crate.
pub mod catalog;

// ── Concrete named strategies (58) ───────────────────────────────────────────
pub mod named;
pub use named::*;

// ── Shared test utilities ─────────────────────────────────────────────────────
#[cfg(test)]
pub mod test_utils;

// ── Tests ─────────────────────────────────────────────────────────────────────
#[cfg(test)]
mod edge_case_tests;
#[cfg(test)]
mod mtf_parity_tests;
#[cfg(test)]
mod rhai_parity_tests;
