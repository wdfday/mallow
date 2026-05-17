use std::collections::{HashMap, VecDeque};

use alm_core::{bar::Bar, Timeframe};
use alm_indicator::IndicatorBox;
use rhai::{Array, Dynamic};

use super::htf::HtfAggregator;
use super::parse::IndicatorKind;

// ── MEntry — custom script type for multi-field indicator values ────────────────

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

    fn is_multi(&self) -> bool { matches!(self, Self::Multi { .. }) }

    fn primary(&self) -> Option<&str> {
        match self { Self::Multi { primary, .. } => Some(primary.as_str()), _ => None }
    }

    fn clear(&mut self) {
        match self {
            Self::Single { data, .. } => data.clear(),
            Self::Multi  { data, .. } => data.clear(),
        }
    }
}

// ── VarBinding ────────────────────────────────────────────────────────────────

pub(super) struct VarBinding {
    pub(super) ind:        IndicatorBox,
    history:               History,
    pub(super) buf_depth:  usize,
    aggregator:            Option<HtfAggregator>,
    // ── live-only ─────────────────────────────────────────────────────────────
    /// `true` → exposes a live (forming) value alongside confirmed history.
    /// Single: `{var}_live: f64`. Multi: `{var}_live: Map`.
    pub(super) live:       bool,
    live_ind:              Option<IndicatorBox>,
    pub(super) live_val:   f64,                  // single only
    pub(super) live_map:   HashMap<String, f64>, // multi only
    pub(super) fill_ratio: f64,
    bucket_m1:             u32,
    tf_total_m1:           u32,
}

impl VarBinding {
    pub(super) fn new(
        ind:       IndicatorBox,
        live_ind:  Option<IndicatorBox>,
        kind:      IndicatorKind,
        buf_depth: usize,
        tf:        Option<Timeframe>,
        live:      bool,
    ) -> Self {
        let tf_total_m1 = tf.map(|t| (t.duration_ms() / 60_000) as u32).unwrap_or(1);
        let aggregator  = tf.map(|t| HtfAggregator::new(t.duration_ms()));
        Self {
            ind,
            history: History::new(kind, buf_depth),
            buf_depth,
            aggregator,
            live,
            live_ind,
            live_val:   0.0,
            live_map:   HashMap::new(),
            fill_ratio: 0.0,
            bucket_m1:  0,
            tf_total_m1,
        }
    }

    pub(super) fn is_multi(&self) -> bool { self.history.is_multi() }

    /// Feed a bar. Returns `true` when the confirmed history buffer is full.
    pub(super) fn update(&mut self, bar: &Bar) -> bool {
        if let Some(agg) = &mut self.aggregator {
            if self.live {
                if let Some(htf_bar) = agg.update(bar) {
                    if let Some(fields) = self.ind.update(&htf_bar) {
                        self.history.push(&fields, self.buf_depth);
                    }
                    self.bucket_m1 = 0;
                }
                self.bucket_m1 += 1;
                self.fill_ratio = (self.bucket_m1 as f64 / self.tf_total_m1 as f64).min(1.0);
                if let Some(forming) = agg.peek() {
                    if let Some(live_ind) = &mut self.live_ind {
                        if let Some(fields) = live_ind.update(&forming) {
                            self.update_live_val(&fields);
                        }
                    }
                }
            } else if let Some(htf_bar) = agg.update(bar) {
                if let Some(fields) = self.ind.update(&htf_bar) {
                    self.history.push(&fields, self.buf_depth);
                }
            } else {
                // Confirmed mode: no HTF bar closed this M1 bar — skip evaluation.
                // The script only runs at HTF boundaries, not on every M1 bar.
                return false;
            }
        } else if let Some(fields) = self.ind.update(bar) {
            self.history.push(&fields, self.buf_depth);
        }
        self.history.len() >= self.buf_depth
    }

    fn update_live_val(&mut self, fields: &HashMap<String, f64>) {
        match &self.history {
            History::Single { field, .. } => {
                if let Some(&v) = fields.get(field.as_str()) { self.live_val = v; }
            }
            History::Multi { .. } => { self.live_map = fields.clone(); }
        }
    }

    /// Script array of confirmed values, newest at index 0.
    /// Single → `Array<f64>`, Multi → `Array<Map>`.
    pub(super) fn to_script_array(&self) -> Array {
        self.history.to_script_array()
    }

    /// For live multi-output: expose `{name}_live` as an `MEntry` Dynamic.
    pub(super) fn live_entry_as_dynamic(&self) -> Dynamic {
        let primary = self.history.primary().unwrap_or("value").to_string();
        Dynamic::from(MEntry::new(self.live_map.clone(), primary))
    }

    pub(super) fn reset(&mut self) {
        self.ind.reset();
        self.history.clear();
        if let Some(agg) = &mut self.aggregator { agg.reset(); }
        if let Some(li)  = &mut self.live_ind   { li.reset(); }
        self.live_val   = 0.0;
        self.live_map.clear();
        self.fill_ratio = 0.0;
        self.bucket_m1  = 0;
    }
}
