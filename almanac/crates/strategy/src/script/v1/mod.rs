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


pub mod strategy;
pub use strategy::{ScriptStrategy, script_indicator_deps};

// ── Shared internals — accessible to v2 and script::lint ─────────────────────
pub(crate) use binding::MEntry;
pub(crate) use crate::script::utils::{scalar_out, bool_out};
pub(crate) use crate::script::engine::{
    build_engine, extract_max_lookback, BAR_FIELDS, DEFAULT_BUF_DEPTH,
    eval_const_int_expr, second_arg_is_static_literal,
};
pub(crate) use parse::{
    extract_candle_directives, extract_regime_block, CandleDirective,
    try_parse_indicator_line, IndicatorKind,
    map_indicator_type, positional_param_names, indicator_json_config,
    PERIOD_EXEMPT,
};
