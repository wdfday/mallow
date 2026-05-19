use std::collections::{HashMap, VecDeque};

use alm_core::bar::Bar;
use alm_indicator::IndicatorBox;
use rhai::{Array, Dynamic};

use super::parse::IndicatorKind;

// ── MEntry — custom script type for multi-field indicator values ──────────────

/// One bar's worth of multi-field indicator output (e.g. `supertrend`, `macd`).
///
/// Registered in the script engine so that:
/// - `macd[0].histogram`  → property getter (exact field name)
/// - `macd[0] > 0`        → comparison uses the semantic primary field (`.macd`)
/// - `rising(supertrend)` → uses the primary field (`"value"`) for each element
#[derive(Debug, Clone)]
pub(crate) struct MEntry {
    pub(crate) fields:  HashMap<String, f64>,
    /// The field that acts as the implicit numeric value for direct comparisons.
    pub(crate) primary: String,
}

impl MEntry {
    pub(crate) fn new(fields: HashMap<String, f64>, primary: String) -> Self {
        Self { fields, primary }
    }

    /// Returns the primary field value, or `0.0` if absent.
    pub(crate) fn primary_value(&self) -> f64 {
        self.fields.get(self.primary.as_str()).copied().unwrap_or(0.0)
    }

    pub(crate) fn field(&self, name: &str) -> f64 {
        self.fields.get(name).copied().unwrap_or(0.0)
    }
}

// ── History ───────────────────────────────────────────────────────────────────

enum History {
    /// Single-field extract → `Array<f64>`.
    Single { field: String, data: VecDeque<f64> },
    /// Full field map → `Array<MEntry>`, e.g. `macd[0].histogram`.
    Multi { primary: String, data: VecDeque<HashMap<String, f64>> },
}

impl History {
    fn new(kind: IndicatorKind, capacity: usize) -> Self {
        match kind {
            IndicatorKind::Single(field) =>
                Self::Single { field, data: VecDeque::with_capacity(capacity) },
            IndicatorKind::Multi(primary) =>
                Self::Multi { primary, data: VecDeque::with_capacity(capacity) },
        }
    }

    fn push(&mut self, fields: &HashMap<String, f64>, cap: usize) {
        match self {
            Self::Single { field, data } => {
                if let Some(&v) = fields.get(field.as_str()) {
                    data.push_back(v);
                    if data.len() > cap { data.pop_front(); }
                }
            }
            Self::Multi { data, .. } => {
                data.push_back(fields.clone());
                if data.len() > cap { data.pop_front(); }
            }
        }
    }

    fn len(&self) -> usize {
        match self {
            Self::Single { data, .. } => data.len(),
            Self::Multi  { data, .. } => data.len(),
        }
    }

    fn to_script_array(&self) -> Array {
        match self {
            Self::Single { data, .. } =>
                data.iter().rev().map(|&v| Dynamic::from_float(v)).collect(),
            Self::Multi { primary, data } =>
                data.iter().rev()
                    .map(|fields| Dynamic::from(MEntry::new(fields.clone(), primary.clone())))
                    .collect(),
        }
    }

    fn clear(&mut self) {
        match self {
            Self::Single { data, .. } => data.clear(),
            Self::Multi  { data, .. } => data.clear(),
        }
    }
}

// ── VarBinding ────────────────────────────────────────────────────────────────

/// Single-TF indicator binding. Feeds every base-TF bar directly into the
/// indicator and keeps a rolling history buffer for the Rhai scope.
///
/// TF and live arguments in indicator declarations (`ind.ema(20, "H1")`,
/// `ind.rsi(5, "live_H1")`) are parsed by `try_parse_indicator_line` so that
/// `ScriptStrategy::build` can reject them with a clear error — V1 itself
/// never sees a binding with `timeframe.is_some()`. Use `MtfScriptStrategy`
/// for real multi-timeframe evaluation.
pub(super) struct VarBinding {
    pub(super) ind:       IndicatorBox,
    history:              History,
    pub(super) buf_depth: usize,
}

impl VarBinding {
    pub(super) fn new(ind: IndicatorBox, kind: IndicatorKind, buf_depth: usize) -> Self {
        Self {
            ind,
            history: History::new(kind, buf_depth),
            buf_depth,
        }
    }

    pub(super) fn is_multi(&self) -> bool {
        matches!(self.history, History::Multi { .. })
    }

    /// Feed a bar. Returns `true` when the history buffer is full.
    pub(super) fn update(&mut self, bar: &Bar) -> bool {
        if let Some(fields) = self.ind.update(bar) {
            self.history.push(&fields, self.buf_depth);
        }
        self.history.len() >= self.buf_depth
    }

    /// Script array of confirmed values, newest at index 0.
    /// Single → `Array<f64>`, Multi → `Array<Map>`.
    pub(super) fn to_script_array(&self) -> Array {
        self.history.to_script_array()
    }

    pub(super) fn reset(&mut self) {
        self.ind.reset();
        self.history.clear();
    }
}
