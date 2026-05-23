//! Single-TF script strategy.
//!
//! Indicator bindings (`binding.rs`) handle HTF declarations by running an
//! internal `HtfAggregator` (see `htf.rs`) — base-TF bars are aggregated into
//! confirmed HTF bars on the fly. This is the correct approach for backtest
//! replay through a single M1 bar feed (`Engine`).
//!
//! For live evaluation, the registry uses the V2 path (`MtfScriptStrategy`) if
//! HTF indicators are declared; V1 handles single-TF scripts only.

mod binding;
mod parse;
mod engine;

pub mod strategy;
pub use strategy::{ScriptStrategy, script_indicator_deps};

pub mod stream;
pub use stream::{ScriptStreamEval, StreamDecl, IndicatorSnapshot, PlotResult};

// ── Shared internals — accessible to v2 and script::lint ─────────────────────
pub(crate) use binding::MEntry;
pub(crate) use engine::{
    build_engine, extract_max_lookback, BAR_FIELDS, DEFAULT_BUF_DEPTH,
};
pub(crate) use parse::{
    extract_candle_directives, extract_regime_block, CandleDirective,
    try_parse_indicator_line, IndicatorKind,
};
