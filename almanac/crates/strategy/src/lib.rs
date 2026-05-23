// ── Factory ───────────────────────────────────────────────────────────────────
pub mod factory;
pub use factory::build_strategy;

// ── MTF Factory ───────────────────────────────────────────────────────────────
pub mod mtf_factory;
pub use mtf_factory::{build_mtf_strategy, MTF_STRATEGY_KEYS};

// ── Candle transform ──────────────────────────────────────────────────────────
pub mod candle_type;
pub use candle_type::{CandleTransform, CandleType};

// ── Expression / template strategies ─────────────────────────────────────────
pub mod script;
pub use script::{
    ScriptStrategy, script_indicator_deps,
    ScriptStreamEval, StreamDecl, IndicatorSnapshot, PlotResult,
    script_lint, LintDiagnostic, ScriptLintScope, DeclaredIndicator, KNOWN_INDICATOR_TYPES,
    MtfScriptStrategy, probe_script_htfs,
};

// ── Bar resampler (MTF helper) ────────────────────────────────────────────────
pub mod bar_resampler;

// ── Strategy / indicator catalogue ────────────────────────────────────────────
// Static metadata surfaced by HTTP (`GET /api/indicators`, `/api/strategies`)
// and used by docs / UI pickers. Lives in strategy because the source of
// truth for what strategies + indicators exist is this crate.
pub mod catalog;

// ── Position sizing / risk managers ──────────────────────────────────────────
pub mod risk;
pub use risk::{AnySizer, AtrSizing, EqualWeight, FixedFractional, FixedQuantity, FixedUsd, KellySizing};

// ── Concrete named strategies (58) ───────────────────────────────────────────
pub mod named;
pub use named::*;

// ── Shared test utilities ─────────────────────────────────────────────────────
#[cfg(test)]
pub mod test_utils;

// ── Tests ─────────────────────────────────────────────────────────────────────
#[cfg(test)]
mod mtf_parity_tests;
#[cfg(test)]
mod script_parity_tests;
#[cfg(test)]
mod named_real_data_parity_tests;
