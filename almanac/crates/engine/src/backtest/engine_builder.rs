//! Build a ready-to-run `Engine` from a `BacktestRequest`.

use std::collections::HashMap;

use alm_core::{
    exit::{ExitRules, IntraBarMode},
    portfolio::PortfolioSnapshot,
    regime::RegimeState,
    signal::{Direction, Signal},
    strategy::Strategy,
    Bar,
};
use alm_strategy::{
    candle_type::{CandleTransform, CandleType},
    build_strategy, AnySizer, FixedFractional, FixedQuantity, FixedUsd,
};
use anyhow::Result;
use serde_json::Value;

use crate::bus_sync::SyncBus;
use crate::types::BacktestRequest;
use crate::Engine;

use super::{DEFAULT_COMMISSION, DEFAULT_CAPITAL, DEFAULT_POSITION_PCT, DEFAULT_SLIPPAGE};

// ── StrengthFilter ────────────────────────────────────────────────────────────

/// Wraps any `Strategy` and discards entry/exit signals below `min` strength.
/// Close signals are always forwarded regardless of strength.
pub struct StrengthFilter {
    pub inner: Box<dyn Strategy>,
    pub min: f64,
}

impl Strategy for StrengthFilter {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        let sigs = self.inner.on_bar(bar);
        if self.min <= 0.0 {
            return sigs;
        }
        sigs.into_iter()
            .filter(|s| s.direction == Direction::Exit || s.strength >= self.min)
            .collect()
    }

    fn on_window(&mut self, bars: &[Bar]) -> Vec<Signal> {
        let sigs = self.inner.on_window(bars);
        if self.min <= 0.0 {
            return sigs;
        }
        sigs.into_iter()
            .filter(|s| s.direction == Direction::Exit || s.strength >= self.min)
            .collect()
    }

    fn name(&self) -> &str {
        self.inner.name()
    }

    fn reset(&mut self) {
        self.inner.reset();
    }

    fn set_portfolio_snapshot(&mut self, snapshot: &PortfolioSnapshot) {
        self.inner.set_portfolio_snapshot(snapshot);
    }

    fn on_regime(&mut self, regime: &RegimeState) {
        self.inner.on_regime(regime);
    }

    fn take_indicator_series(&mut self) -> HashMap<String, Vec<(i64, f64)>> {
        self.inner.take_indicator_series()
    }

    fn current_regime(&self) -> Option<&RegimeState> {
        self.inner.current_regime()
    }

    fn script_candle_spec(&self) -> Option<(String, Option<usize>)> {
        self.inner.script_candle_spec()
    }

    fn handles_candle_internally(&self) -> bool {
        self.inner.handles_candle_internally()
    }
}

// ── Engine builder ────────────────────────────────────────────────────────────

/// Build a configured `Engine` from a backtest request.
/// Extracts capital, sizing, commission, slippage, candle type, and exit rules.
pub fn build(
    req: &BacktestRequest,
    params: &Value,
) -> Result<Engine<StrengthFilter, AnySizer, SyncBus>> {
    let capital = req.initial_capital.unwrap_or(DEFAULT_CAPITAL);
    let commission = req.commission_pct.unwrap_or(DEFAULT_COMMISSION);
    let slippage = req.slippage_pct.unwrap_or(DEFAULT_SLIPPAGE);
    let min_strength = req.min_strength.unwrap_or(0.0);
    let lot_size = asset_lot_size(req.asset_type.as_deref());
    let max_positions = req.max_positions.unwrap_or(1).max(1);

    let inner = build_strategy(&req.strategy, params)?;
    let strategy = StrengthFilter { inner, min: min_strength };

    let risk: AnySizer = if let Some(qty) = req.position_size_quantity {
        AnySizer::FixedQuantity(FixedQuantity::new(qty, max_positions))
    } else if let Some(usd) = req.position_size_usd {
        AnySizer::FixedUsd(FixedUsd::new(usd, max_positions).with_lot_size(lot_size))
    } else {
        let pct = req
            .position_size_pct
            .unwrap_or(DEFAULT_POSITION_PCT)
            .clamp(0.01, 1.0);
        AnySizer::FixedFractional(FixedFractional::new(pct, max_positions).with_lot_size(lot_size))
    };

    let exit_rules = ExitRules {
        intra_bar_mode: intra_bar_mode_from_str(req.intra_bar_mode.as_deref()),
        ..Default::default()
    };

    // Engine-level candle transform: always Raw. Script strategies apply their
    // own `candle.transform(...)` directive internally inside on_bar (so they
    // work the same in backtest and live registry); named strategies operate
    // on raw bars by design.
    Ok(Engine::sync(capital, strategy, risk, commission, slippage)
        .with_exit_rules(exit_rules)
        .with_candle_transform(CandleTransform::new(CandleType::Raw)))
}

// ── Utilities ─────────────────────────────────────────────────────────────────

pub fn intra_bar_mode_from_str(s: Option<&str>) -> IntraBarMode {
    match s {
        Some("pessimistic") => IntraBarMode::Pessimistic,
        Some("ohlc_heuristic") => IntraBarMode::OhlcHeuristic,
        _ => IntraBarMode::CloseOnly,
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
