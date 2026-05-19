//! Rhai engine builder for v2 scripts.
//!
//! Delegates directly to v1's [`build_engine`] so that the same [`MEntry`]
//! type is registered in both v1 and v2 script engines — a hard requirement
//! because `to_script_array()` in v2's [`FeedVarBinding`] produces `MEntry`
//! values and the engine must know the type for property getters to work.

pub(super) use crate::script::v1::{
    build_engine,
    BAR_FIELDS,
    PlotBuf,
};
