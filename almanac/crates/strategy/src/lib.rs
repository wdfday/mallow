// ── Factory ───────────────────────────────────────────────────────────────────
pub mod factory;
pub use factory::build_strategy;

// ── Expression / template strategies ─────────────────────────────────────────
pub mod expr;
pub use expr::CelStrategy;

// ── Declarative JSON strategy ─────────────────────────────────────────────────
pub mod dynamic;
pub use dynamic::DynamicStrategy;

// ── Concrete named strategies (58) ───────────────────────────────────────────
pub mod named;
pub use named::*;

// ── Tests ─────────────────────────────────────────────────────────────────────
#[cfg(test)]
mod parity_tests;

#[cfg(test)]
mod edge_case_tests;
