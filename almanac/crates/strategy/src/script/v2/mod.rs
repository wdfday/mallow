//! Script strategy v2 — **multi-bus, clone-and-project live forming**.
//!
//! # What's different from [`v1`][crate::script::v1]
//!
//! | Concern | v1 | **v2** |
//! |---|---|---|
//! | HTF source | Resample M1 → HTF via `HtfAggregator` inside each binding | Bind to real HTF feed via [`alm_engine::MtfEngine`] |
//! | Live "forming" | Parallel `live_ind: IndicatorBox` updated every M1 | Clone-and-project: clone canonical `ind`, feed peek of forming bucket |
//! | Indicator instances per binding | 2 (`ind` confirmed + `live_ind` for forming) | 1 (`ind` only — clone is throwaway) |
//! | Drift between confirmed and live | Possible (separate state) | None (live is "what would `ind` be if I update it with peek-close?") |
//! | Confirmed advance trigger | Bucket boundary detected during base bar flow | Real HTF feed bar arrival |
//!
//! # Public surface
//!
//! - [`MtfScriptStrategy`] — main type. Implements [`alm_core::MtfStrategy`] for
//!   use with [`alm_engine::MtfEngine`].
//! - [`MtfScriptStrategy::declared_htfs`] — discover which TF feeds the script needs.

pub(in crate::script) mod live_agg;
pub(in crate::script) mod feed_binding;
pub(in crate::script) mod parse;
pub(in crate::script) mod engine;
pub mod strategy;

pub use strategy::MtfScriptStrategy;
