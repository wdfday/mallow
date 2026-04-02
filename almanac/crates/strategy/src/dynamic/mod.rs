pub mod condition;
pub mod indicator_box;

use std::collections::HashMap;

use anyhow::{bail, Result};
use alm_core::{Bar, Signal, Strategy};
use serde_json::Value;

use self::condition::ConditionGroup;
use self::indicator_box::IndicatorBox;

/// Internal state for one named indicator.
struct IndicatorState {
    box_: IndicatorBox,
    current: HashMap<String, f64>,
    previous: HashMap<String, f64>,
}

impl IndicatorState {
    fn new(box_: IndicatorBox) -> Self {
        Self {
            box_,
            current: HashMap::new(),
            previous: HashMap::new(),
        }
    }

    fn update(&mut self, bar: &Bar) {
        if let Some(vals) = self.box_.update(bar) {
            self.previous = std::mem::replace(&mut self.current, vals);
        }
    }

    fn reset(&mut self) {
        self.box_.reset();
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
pub struct DynamicStrategy {
    indicators: HashMap<String, IndicatorState>,
    entry: ConditionGroup,
    exit: Option<ConditionGroup>,
    in_position: bool,
}

impl DynamicStrategy {
    pub fn from_params(params: &Value) -> Result<Self> {
        // ── Indicators ────────────────────────────────────────────────────────
        let ind_map = params
            .get("indicators")
            .and_then(Value::as_object)
            .ok_or_else(|| anyhow::anyhow!("DynamicStrategy params missing 'indicators' object"))?;

        let mut indicators = HashMap::new();
        for (name, cfg) in ind_map {
            let box_ = IndicatorBox::from_config(cfg)?;
            indicators.insert(name.clone(), IndicatorState::new(box_));
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
        // Advance all indicators
        for state in self.indicators.values_mut() {
            state.update(bar);
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
        self.in_position = false;
    }
}
