//! Expression / template strategies — define entry/exit logic as text.
//!
//! | Module  | Engine         | Syntax           | Status  |
//! |---------|----------------|------------------|---------|
//! | `rhai`  | Rhai scripting | `ind.ema(9)[0]`  | Active  |

pub mod rhai_strategy;
pub use rhai_strategy::{
    RhaiStrategy,
    rhai_lint, LintDiagnostic, RhaiLintScope, DeclaredIndicator, KNOWN_INDICATOR_TYPES,
};

pub mod rhai_stream;
pub use rhai_stream::{RhaiStreamEval, StreamDecl, PlotResult};
