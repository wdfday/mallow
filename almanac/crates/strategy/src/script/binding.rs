use std::collections::{HashMap, VecDeque};

use alm_core::{bar::Bar, Timeframe};
use alm_indicator::IndicatorBox;
use rhai::{Array, Dynamic, Map as RhaiMap};

use super::htf::HtfAggregator;
use super::parse::IndicatorKind;

// ── History ───────────────────────────────────────────────────────────────────

enum History {
    /// Single-field extract → Rhai `Array<f64>`.
    Single { field: String, data: VecDeque<f64> },
    /// Full field map → Rhai `Array<Map>`, e.g. `macd[0].histogram`.
    Multi(VecDeque<HashMap<String, f64>>),
}

impl History {
    fn new(kind: IndicatorKind, capacity: usize) -> Self {
        match kind {
            IndicatorKind::Single(field) => Self::Single { field, data: VecDeque::with_capacity(capacity) },
            IndicatorKind::Multi         => Self::Multi(VecDeque::with_capacity(capacity)),
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
            Self::Multi(data) => {
                data.push_back(fields.clone());
                if data.len() > cap { data.pop_front(); }
            }
        }
    }

    fn len(&self) -> usize {
        match self { Self::Single { data, .. } => data.len(), Self::Multi(data) => data.len() }
    }

    fn to_rhai_array(&self) -> Array {
        match self {
            Self::Single { data, .. } =>
                data.iter().rev().map(|&v| Dynamic::from_float(v)).collect(),
            Self::Multi(data) =>
                data.iter().rev().map(|fields| Dynamic::from_map(fields_to_rhai(fields))).collect(),
        }
    }

    fn is_multi(&self) -> bool { matches!(self, Self::Multi(_)) }

    fn clear(&mut self) {
        match self { Self::Single { data, .. } => data.clear(), Self::Multi(data) => data.clear() }
    }
}

fn fields_to_rhai(fields: &HashMap<String, f64>) -> RhaiMap {
    fields.iter().map(|(k, &v)| (k.clone().into(), Dynamic::from_float(v))).collect()
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
            History::Multi(_) => { self.live_map = fields.clone(); }
        }
    }

    /// Rhai array of confirmed values, newest at index 0.
    /// Single → `Array<f64>`, Multi → `Array<Map>`.
    pub(super) fn to_rhai_array(&self) -> Array {
        self.history.to_rhai_array()
    }

    /// For live multi-output: expose `{name}_live` as a Rhai Map.
    pub(super) fn live_map_as_rhai(&self) -> RhaiMap {
        fields_to_rhai(&self.live_map)
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
