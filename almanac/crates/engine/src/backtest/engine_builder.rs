//! Build a ready-to-run `Engine` from a `BacktestRequest`.

use alm_core::{
    exit::IntraBarMode,
    strategy::Strategy,
};
use alm_strategy::build_strategy;
use crate::risk::{AnySizer, AtrSizing, FixedFractional, FixedQuantity, FixedUsd, RiskFractional};
use anyhow::Result;
use serde_json::Value;

use crate::types::BacktestRequest;
use crate::Engine;

use super::{DEFAULT_COMMISSION, DEFAULT_CAPITAL, DEFAULT_POSITION_PCT, DEFAULT_SLIPPAGE};

// ── Engine builder ────────────────────────────────────────────────────────────

/// Build a configured `Engine` from a backtest request.
/// Extracts capital, sizing, commission, slippage, and exit rules.
pub fn build(
    req: &BacktestRequest,
    params: &Value,
) -> Result<Engine<Box<dyn Strategy>, AnySizer>> {
    let capital = req.initial_capital.unwrap_or(DEFAULT_CAPITAL);
    let commission = req.commission_pct.unwrap_or(DEFAULT_COMMISSION);
    let slippage = req.slippage_pct.unwrap_or(DEFAULT_SLIPPAGE);
    let lot_size = asset_lot_size(req.asset_type.as_deref());
    let max_positions = req.max_positions.unwrap_or(1).max(1);

    let strategy = build_strategy(&req.strategy, params)?;

    let risk = select_sizer(req, max_positions, lot_size);

    let intra_bar_mode = intra_bar_mode_from_str(req.intra_bar_mode.as_deref());
    let max_units = req.max_units.unwrap_or(1).max(1);

    // Exit levels (SL/TP, trailing, max-bars) travel with each Signal; the engine
    // only needs the intra-bar fill mode here. Script strategies apply their own
    // `candle.transform(...)` directive internally inside on_bar (so they behave
    // the same in backtest and live registry); named strategies operate on raw bars.
    let mut engine = Engine::sync(capital, strategy, risk, commission, slippage)
        .with_intra_bar_mode(intra_bar_mode);
    if let Some(ref policy_str) = req.reverse_policy {
        let policy = match policy_str.as_str() {
            "exit" => Some(crate::engine::ReversePolicy::Exit),
            "flip" => Some(crate::engine::ReversePolicy::Flip),
            _ => None,
        };
        if let Some(p) = policy {
            engine = engine.with_reverse_policy(p);
        }
    }
    if max_units > 1 {
        engine = engine.with_pyramiding(max_units, req.max_position_pct.unwrap_or(0.0));
        // pyramid == Some(false) → independent legs; true/None → merge (default).
        if req.pyramid == Some(false) {
            engine = engine.with_independent_legs();
        }
    }
    Ok(engine)
}

/// Like [`build`] but accepts a pre-built strategy (e.g. with a shared error sink).
pub fn build_with_strategy(
    req: &BacktestRequest,
    strategy: Box<dyn Strategy>,
) -> Result<Engine<Box<dyn Strategy>, AnySizer>> {
    let capital = req.initial_capital.unwrap_or(DEFAULT_CAPITAL);
    let commission = req.commission_pct.unwrap_or(DEFAULT_COMMISSION);
    let slippage = req.slippage_pct.unwrap_or(DEFAULT_SLIPPAGE);
    let lot_size = asset_lot_size(req.asset_type.as_deref());
    let max_positions = req.max_positions.unwrap_or(1).max(1);

    let risk = select_sizer(req, max_positions, lot_size);

    let intra_bar_mode = intra_bar_mode_from_str(req.intra_bar_mode.as_deref());
    let max_units = req.max_units.unwrap_or(1).max(1);

    let mut engine = Engine::sync(capital, strategy, risk, commission, slippage)
        .with_intra_bar_mode(intra_bar_mode);
    if let Some(ref policy_str) = req.reverse_policy {
        let policy = match policy_str.as_str() {
            "exit" => Some(crate::engine::ReversePolicy::Exit),
            "flip" => Some(crate::engine::ReversePolicy::Flip),
            _ => None,
        };
        if let Some(p) = policy {
            engine = engine.with_reverse_policy(p);
        }
    }
    if max_units > 1 {
        engine = engine.with_pyramiding(max_units, req.max_position_pct.unwrap_or(0.0));
        // pyramid == Some(false) → independent legs; true/None → merge (default).
        if req.pyramid == Some(false) {
            engine = engine.with_independent_legs();
        }
    }
    Ok(engine)
}

/// Map a backtest request's sizing fields to a concrete [`AnySizer`].
///
/// Mirrors helm's 5 `SizeMode` values. When `req.size_mode` is set it dispatches
/// explicitly (the canonical path); otherwise it falls back to legacy field
/// inference. `strength_sizing` is an ORTHOGONAL tag (scale by `signal.strength`),
/// not a mode. Mode → sizer:
///   * `fixed_fractional` → `RiskFractional` — Ralph Vince: risk f% to the signal's stop
///   * `volatility`       → `AtrSizing`       — risk f% to an ATR stop
///   * `percent_equity`   → `FixedFractional` — % of equity allocation
///   * `quote_qty`        → `FixedUsd`
///   * `fixed_qty`        → `FixedQuantity`
fn select_sizer(req: &BacktestRequest, max_positions: usize, lot_size: f64) -> AnySizer {
    build_sizer(
        req.size_mode.as_deref(),
        req.position_size_quantity,
        req.position_size_usd,
        req.risk_per_trade_pct,
        req.atr_multiplier,
        req.position_size_pct,
        req.strength_sizing,
        max_positions,
        lot_size,
    )
}

/// Field-explicit sizer builder shared by every backtest entry point
/// (single-TF [`build`]/[`build_with_strategy`] and the MTF runners in
/// `backtest::mod`). Keeps the sizing taxonomy defined in exactly one place.
#[allow(clippy::too_many_arguments)]
pub(crate) fn build_sizer(
    size_mode: Option<&str>,
    position_size_quantity: Option<f64>,
    position_size_usd: Option<f64>,
    risk_per_trade_pct: Option<f64>,
    atr_multiplier: Option<f64>,
    position_size_pct: Option<f64>,
    strength_sizing: Option<bool>,
    max_positions: usize,
    lot_size: f64,
) -> AnySizer {
    let strength = strength_sizing.unwrap_or(true);
    let atr_mult = atr_multiplier.unwrap_or(2.0);
    let risk = risk_per_trade_pct.unwrap_or(0.01);
    let pct = position_size_pct.unwrap_or(DEFAULT_POSITION_PCT).clamp(0.01, 1.0);

    // Explicit mode (synced with helm SizeMode) — the canonical path.
    match size_mode {
        Some("fixed_qty") =>
            return AnySizer::FixedQuantity(FixedQuantity::new(position_size_quantity.unwrap_or(1.0), max_positions)),
        Some("quote_qty") =>
            return AnySizer::FixedUsd(FixedUsd::new(position_size_usd.unwrap_or(0.0), max_positions).with_lot_size(lot_size)),
        // Risk-based modes already pin a fixed risk to the stop — scaling by strength
        // would break that invariant, so strength is forced off (it's a percent_equity-only tag).
        Some("volatility") =>
            return AnySizer::Atr(AtrSizing::new(risk, atr_mult, max_positions).with_strength_sizing(false)),
        Some("fixed_fractional") =>
            return AnySizer::RiskFractional(RiskFractional::new(risk, max_positions).with_lot_size(lot_size).with_strength_sizing(false)),
        Some("percent_equity") =>
            return AnySizer::FixedFractional(FixedFractional::new(pct, max_positions).with_lot_size(lot_size).with_strength_sizing(strength)),
        _ => {} // fall through to legacy field inference
    }

    // Legacy field inference (no explicit size_mode) — keeps old requests working.
    if let Some(qty) = position_size_quantity {
        AnySizer::FixedQuantity(FixedQuantity::new(qty, max_positions))
    } else if let Some(usd) = position_size_usd {
        AnySizer::FixedUsd(FixedUsd::new(usd, max_positions).with_lot_size(lot_size))
    } else if risk_per_trade_pct.is_some() {
        AnySizer::Atr(AtrSizing::new(risk, atr_mult, max_positions).with_strength_sizing(false))
    } else {
        AnySizer::FixedFractional(
            FixedFractional::new(pct, max_positions).with_lot_size(lot_size).with_strength_sizing(strength),
        )
    }
}

// ── Utilities ─────────────────────────────────────────────────────────────────

pub fn intra_bar_mode_from_str(s: Option<&str>) -> IntraBarMode {
    match s {
        Some("pessimistic") => IntraBarMode::Pessimistic,
        Some("ohlc_heuristic") => IntraBarMode::OhlcHeuristic,
        _ => IntraBarMode::Pessimistic,
    }
}

/// Map `asset_type` string → lot size for position sizing.
/// `"crypto"` / `None` → 0.0 (fractional), `"stock"` → 1.0, `"vn_stock"` → 100.0.
pub fn asset_lot_size(asset_type: Option<&str>) -> f64 {
    match asset_type {
        Some("stock") => 1.0,
        Some("vn_stock") => 100.0,
        _ => 0.0,
    }
}

#[cfg(test)]
mod sizer_tests {
    use super::*;

    /// Legacy field-inference (no explicit size_mode) maps to the AnySizer taxonomy.
    #[test]
    fn build_sizer_maps_every_mode() {
        // fixed_qty
        assert!(matches!(
            build_sizer(None, Some(2.0), None, None, None, None, None, 1, 0.0),
            AnySizer::FixedQuantity(_)
        ));
        // quote_qty (fixed USD) — takes precedence over a pct fallback
        assert!(matches!(
            build_sizer(None, None, Some(500.0), None, None, Some(0.5), None, 1, 0.0),
            AnySizer::FixedUsd(_)
        ));
        // volatility (ATR) — legacy: risk_per_trade present → ATR
        assert!(matches!(
            build_sizer(None, None, None, Some(0.01), Some(2.0), None, None, 1, 0.0),
            AnySizer::Atr(_)
        ));
        // percent_equity (pct) fallback
        assert!(matches!(
            build_sizer(None, None, None, None, None, Some(0.5), None, 1, 0.0),
            AnySizer::FixedFractional(_)
        ));
        // default (no field set) → FixedFractional
        assert!(matches!(
            build_sizer(None, None, None, None, None, None, None, 1, 0.0),
            AnySizer::FixedFractional(_)
        ));
    }

    /// Explicit size_mode (synced with helm SizeMode) dispatches to the right sizer.
    /// Crucially `fixed_fractional` = Ralph Vince risk-based (`RiskFractional`), distinct
    /// from `volatility` (ATR) even though both read risk_per_trade_pct.
    #[test]
    fn build_sizer_explicit_mode() {
        let m = |mode| build_sizer(Some(mode), Some(1.0), Some(500.0), Some(0.01), Some(2.0), Some(0.5), None, 1, 0.0);
        assert!(matches!(m("fixed_fractional"), AnySizer::RiskFractional(_)));
        assert!(matches!(m("volatility"),       AnySizer::Atr(_)));
        assert!(matches!(m("percent_equity"),   AnySizer::FixedFractional(_)));
        assert!(matches!(m("quote_qty"),        AnySizer::FixedUsd(_)));
        assert!(matches!(m("fixed_qty"),        AnySizer::FixedQuantity(_)));
    }
}
