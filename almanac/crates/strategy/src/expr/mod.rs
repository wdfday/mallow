//! Expression / template strategies — define entry/exit logic as text.
//!
//! | Module  | Engine          | Syntax              |
//! |---------|-----------------|---------------------|
//! | `cel`   | cel-interpreter | `ema(9)`, `rsi(14)` |
//!
//! For declarative JSON-based strategies see [`crate::dynamic`].

pub mod cel;
pub use cel::{CelScript, CelStrategy, parse_cel_indicator};

pub mod rhai_strategy;
pub use rhai_strategy::{
    RhaiStrategy,
    rhai_lint, LintDiagnostic, RhaiLintScope, DeclaredIndicator, KNOWN_INDICATOR_TYPES,
};

pub mod rhai_stream;
pub use rhai_stream::{RhaiStreamEval, StreamDecl, PlotResult};
