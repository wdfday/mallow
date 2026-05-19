//! Factory for named MTF strategies — parallel to [`crate::factory`] for single-TF.

use alm_core::MtfStrategy;
use anyhow::{bail, Result};
use serde_json::Value;

use crate::named::{
    MtfAdxPullbackStrategy, MtfBbMacdStrategy, MtfEmaRsiStrategy, MtfMaCrossStrategy,
};

/// All registered MTF strategy keys. Returned by `GET /api/v1/strategies`.
pub const MTF_STRATEGY_KEYS: &[&str] = &[
    "mtf_ema_rsi",
    "mtf_ma_cross",
    "mtf_adx_pullback",
    "mtf_bb_macd",
];

/// Build a boxed [`MtfStrategy`] from a strategy key and a flat JSON params object.
///
/// Currently all MTF strategies use hardcoded defaults; `params` is accepted for
/// future extensibility and is ignored for now.
pub fn build_mtf_strategy(name: &str, _params: &Value) -> Result<Box<dyn MtfStrategy>> {
    let s: Box<dyn MtfStrategy> = match name {
        "mtf_ema_rsi"      => Box::new(MtfEmaRsiStrategy::new()),
        "mtf_ma_cross"     => Box::new(MtfMaCrossStrategy::new()),
        "mtf_adx_pullback" => Box::new(MtfAdxPullbackStrategy::new()),
        "mtf_bb_macd"      => Box::new(MtfBbMacdStrategy::new()),
        other => bail!(
            "unknown MTF strategy '{other}'. Known keys: {}",
            MTF_STRATEGY_KEYS.join(", ")
        ),
    };
    Ok(s)
}
