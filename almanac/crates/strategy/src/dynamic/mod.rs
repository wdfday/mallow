pub mod condition;
pub mod indicator_box;

use std::collections::HashMap;

use anyhow::{bail, Result};
use alm_core::{Bar, Signal, Strategy};
use serde_json::Value;

use self::condition::ConditionGroup;
use self::indicator_box::IndicatorBox;
use crate::bar_resampler::{TimeBarResampler, parse_timeframe_ms};
use crate::candle_type::{CandleTransform, CandleType};

/// Internal state for one named indicator.
struct IndicatorState {
    box_: IndicatorBox,
    /// Time-based resampler — present when `"tf"` is set in indicator config.
    resampler: Option<TimeBarResampler>,
    current: HashMap<String, f64>,
    previous: HashMap<String, f64>,
}

impl IndicatorState {
    fn new(box_: IndicatorBox) -> Self {
        Self { box_, resampler: None, current: HashMap::new(), previous: HashMap::new() }
    }

    /// Attach a time-based resampler using a TradingView-style timeframe string.
    /// `"H1"` → H1 bars, `"M15"` → M15 bars, `"D1"` → daily bars.
    /// Returns `self` unchanged if `tf` is unrecognised.
    fn with_tf(mut self, tf: &str) -> Self {
        if let Some(ms) = parse_timeframe_ms(tf) {
            self.resampler = Some(TimeBarResampler::new(ms));
        }
        self
    }

    fn update(&mut self, bar: &Bar) {
        // Time-based MTF: feed indicator only when the resampler emits a HTF bar.
        // Between HTF bars: hold current (no update) so ready-check stays true.
        let agg = match &mut self.resampler {
            Some(rs) => rs.push(bar),
            None => Some(bar.clone()),
        };
        if let Some(b) = agg {
            if let Some(vals) = self.box_.update(&b) {
                self.previous = std::mem::replace(&mut self.current, vals);
            }
        }
    }

    fn reset(&mut self) {
        self.box_.reset();
        if let Some(rs) = &mut self.resampler { rs.reset(); }
        self.current.clear();
        self.previous.clear();
    }
}

/// A strategy defined entirely by a JSON config — no Rust code needed for new combinations.
///
/// # JSON shape
/// ```json
/// {
///   "strategy": "dynamic",
///   "params": {
///     "candle_type": "heiken_ashi",
///     "indicators": {
///       "rsi14":  { "type": "rsi",  "period": 14 },
///       "ema200": { "type": "ema",  "period": 200 },
///       "macd":   { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
///     },
///     "entry": {
///       "logic": "and",
///       "rules": [
///         { "source": "rsi14", "field": "value",     "op": "lt",          "value": 30 },
///         { "source": "close", "field": "value",     "op": "gt",          "compare": "ema200" },
///         { "source": "macd",  "field": "histogram", "op": "cross_above", "value": 0 }
///       ]
///     },
///     "exit": {
///       "logic": "or",
///       "rules": [
///         { "source": "rsi14", "field": "value",     "op": "gt", "value": 70 },
///         { "source": "macd",  "field": "histogram", "op": "lt", "value": 0 }
///       ]
///     }
///   }
/// }
/// ```
///
/// ## `candle_type` options
/// - `"raw"` (default) — dùng nến gốc
/// - `"heiken_ashi"` — transform sang Heiken Ashi trước khi feed indicators
/// - `"smooth_ha"` + `"ha_smooth": N` — Smooth Heiken Ashi (EMA N trước HA)
pub struct DynamicStrategy {
    indicators: HashMap<String, IndicatorState>,
    entry: ConditionGroup,
    exit: Option<ConditionGroup>,
    in_position: bool,
    transform: CandleTransform,
}

impl DynamicStrategy {
    pub fn from_params(params: &Value) -> Result<Self> {
        // ── Candle type ───────────────────────────────────────────────────────
        let candle_type_str = params
            .get("candle_type")
            .and_then(Value::as_str)
            .unwrap_or("raw");
        let ha_smooth = params
            .get("ha_smooth")
            .and_then(Value::as_f64)
            .map(|v| v as usize)
            .unwrap_or(2);
        let transform = CandleTransform::new(CandleType::from_str(candle_type_str, ha_smooth));

        // ── Indicators ────────────────────────────────────────────────────────
        let ind_map = params
            .get("indicators")
            .and_then(Value::as_object)
            .ok_or_else(|| anyhow::anyhow!("DynamicStrategy params missing 'indicators' object"))?;

        let mut indicators = HashMap::new();
        for (name, cfg) in ind_map {
            let tf = cfg.get("tf").and_then(Value::as_str).unwrap_or("");
            let box_ = IndicatorBox::from_config(cfg)?;
            let state = IndicatorState::new(box_);
            let state = if tf.is_empty() { state } else { state.with_tf(tf) };
            indicators.insert(name.clone(), state);
        }

        // ── Entry group ───────────────────────────────────────────────────────
        let entry_val = params
            .get("entry")
            .ok_or_else(|| anyhow::anyhow!("DynamicStrategy params missing 'entry'"))?;
        let entry = ConditionGroup::from_value(entry_val)?;

        // ── Optional exit group ───────────────────────────────────────────────
        let exit = params
            .get("exit")
            .map(ConditionGroup::from_value)
            .transpose()?;

        if indicators.is_empty() {
            bail!("DynamicStrategy requires at least one indicator");
        }

        Ok(Self {
            indicators,
            entry,
            exit,
            in_position: false,
            transform,
        })
    }

    /// Build snapshot maps for condition evaluation.
    fn snapshots(
        &self,
        use_current: bool,
    ) -> HashMap<String, HashMap<String, f64>> {
        self.indicators
            .iter()
            .map(|(name, state)| {
                let fields = if use_current {
                    state.current.clone()
                } else {
                    state.previous.clone()
                };
                (name.clone(), fields)
            })
            .collect()
    }

    fn bar_fields(bar: &Bar) -> HashMap<String, f64> {
        let mut m = HashMap::new();
        m.insert("open".into(), bar.open);
        m.insert("high".into(), bar.high);
        m.insert("low".into(), bar.low);
        m.insert("close".into(), bar.close);
        m.insert("volume".into(), bar.volume);
        m
    }
}

impl Strategy for DynamicStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        // Transform candle if needed (HA, etc.)
        let effective = match self.transform.apply(bar) {
            Some(b) => b,
            None => return vec![], // HA warmup period
        };

        // Advance all indicators
        for state in self.indicators.values_mut() {
            state.update(&effective);
        }

        // Check if any indicator is still warming up (empty current map)
        let ready = self
            .indicators
            .values()
            .all(|s| !s.current.is_empty());
        if !ready {
            return vec![];
        }

        let current = self.snapshots(true);
        let previous = self.snapshots(false);
        // bar_fields dùng raw bar để price conditions reflect giá thực tế
        let bf = Self::bar_fields(bar);

        // Exit check first
        if self.in_position {
            if let Some(exit_group) = &self.exit {
                if exit_group.evaluate(&current, &previous, &bf) {
                    self.in_position = false;
                    return vec![Signal::close(bar.timestamp, bar.symbol.clone())];
                }
            }
            return vec![];
        }

        // Entry check
        if self.entry.evaluate(&current, &previous, &bf) {
            self.in_position = true;
            return vec![Signal::long(bar.timestamp, bar.symbol.clone(), 1.0)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "dynamic"
    }

    fn reset(&mut self) {
        for state in self.indicators.values_mut() {
            state.reset();
        }
        self.transform.reset();
        self.in_position = false;
    }
}
