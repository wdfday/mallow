//! Expression / template strategies — define entry/exit logic as text.
//!
//! User-facing surface: script-based strategies authored as text and compiled
//! at runtime. The underlying execution engine is intentionally not exposed.

mod htf;
mod binding;
mod parse;
mod engine;
mod lint;

pub mod strategy;
pub use strategy::{
    ScriptStrategy,
    script_lint, LintDiagnostic, ScriptLintScope, DeclaredIndicator, KNOWN_INDICATOR_TYPES,
};

pub mod stream;
pub use stream::{ScriptStreamEval, StreamDecl, IndicatorSnapshot, PlotResult};
