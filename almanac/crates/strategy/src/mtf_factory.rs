//! Factory for named MTF strategies — parallel to [`crate::factory`] for single-TF.

use alm_core::MtfStrategy;
use anyhow::{bail, Result};
use serde_json::Value;

use crate::named::{
    KitchenSinkStrategy,
    MtfAdxPullbackStrategy, MtfBbMacdStrategy, MtfEmaRsiStrategy, MtfMaCrossStrategy,
};

/// All registered MTF strategy keys. Returned by `GET /api/v1/strategies`.
pub const MTF_STRATEGY_KEYS: &[&str] = &[
    "kitchen_sink",
    "mtf_adx_pullback",
    "mtf_bb_macd",
    "mtf_ema_rsi",
    "mtf_ma_cross",
];

/// Build a boxed [`MtfStrategy`] from a strategy key and a flat JSON params object.
///
/// Accepts an optional `"symbol"` field in `params` for strategies that need it
/// (e.g. `kitchen_sink`). All other params are reserved for future use.
pub fn build_mtf_strategy(name: &str, params: &Value) -> Result<Box<dyn MtfStrategy>> {
    let symbol = params.get("symbol")
        .and_then(|v| v.as_str())
        .unwrap_or("UNKNOWN")
        .to_owned();

    let s: Box<dyn MtfStrategy> = match name {
        "kitchen_sink"     => Box::new(KitchenSinkStrategy::new(symbol)),
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
