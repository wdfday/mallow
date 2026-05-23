//! Script strategies — define entry/exit logic as text, compiled at runtime.
//!
//! # Versioning
//!
//! - [`v1`] — single-TF strategy. Indicators declare their TF
//!   (`ind.ema(20, "H1")`) and the binding aggregates base → HTF internally
//!   via `HtfAggregator`. Compatible with [`alm_engine::Engine`] (single bar
//!   feed). `ScriptStrategy` is the public handle.
//!
//! - [`v2`] — multi-timeframe strategy. HTF indicators bind to real confirmed
//!   bars coming from separate per-TF feeds (live: ledger windows; backtest:
//!   real parquet files via `MtfEngine`). No internal resampling.
//!   `MtfScriptStrategy` is the public handle.
//!
//! # Linter
//!
//! [`script_lint`] is version-agnostic — the `ind.TYPE(period, "TF")` syntax
//! is shared. It lives at this level and covers both v1 and v2 scripts.

pub mod v1;
pub mod v2;

mod lint;
mod probe;

pub use probe::probe_script_htfs;

// ── Lint surface (shared, version-agnostic) ───────────────────────────────────
pub use lint::{
    script_lint, LintDiagnostic, ScriptLintScope, DeclaredIndicator, KNOWN_INDICATOR_TYPES,
};

// ── v1 surface ────────────────────────────────────────────────────────────────

// Back-compat: keep `script::strategy::*` and `script::stream::*` paths alive
// so factory.rs and other callers compile unchanged.
pub use v1::strategy;
pub use v1::stream;

pub use v1::{
    ScriptStrategy, script_indicator_deps,
    ScriptStreamEval, StreamDecl, IndicatorSnapshot, PlotResult,
};

// ── v2 surface ────────────────────────────────────────────────────────────────
pub use v2::MtfScriptStrategy;
