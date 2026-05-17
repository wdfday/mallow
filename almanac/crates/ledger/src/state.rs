//! `SymbolState` — per-(symbol, timeframe) sliding bar window plus
//! one `IndicatorCell` per registered `IndicatorSpec`.

use std::collections::{HashMap, VecDeque};

use alm_core::{Bar, Timeframe};
use alm_indicator::IndicatorBox;
use serde::{Deserialize, Serialize};
use tracing::{debug, trace};

use crate::spec::IndicatorSpec;

/// Flat map of named output fields for a single indicator at a single bar.
/// This is the external / row-oriented view; internally each cell stores
/// its series in columnar form (one `VecDeque<Option<f64>>` per field).
pub type IndicatorFields = HashMap<String, f64>;

/// Push `v` onto `buf`, then pop from the front until `buf.len() <= cap`.
/// `VecDeque` is already a ring buffer under the hood; this helper just
/// bakes in the "fixed-cap sliding window" invariant at the single call
/// site so we can't accidentally forget to trim.
#[inline]
fn push_bounded<T>(buf: &mut VecDeque<T>, v: T, cap: usize) {
    buf.push_back(v);
    while buf.len() > cap {
        buf.pop_front();
    }
}

/// Row-oriented snapshot slice — aligned to `SymbolSnapshot::bars`.
/// Produced by materialising columnar storage into rows. Expensive to
/// build (one `HashMap` alloc per bar); prefer [`IndicatorColumnSlice`]
/// in hot HTTP paths.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IndicatorSeriesSlice {
    pub spec: IndicatorSpec,
    /// `values[i]` corresponds to `bars[i]`. `None` = still warming up.
    pub values: Vec<Option<IndicatorFields>>,
    /// Timestamp of the first `Some` entry, or `None` if still warming up.
    pub ready_since_t: Option<i64>,
}

/// Column-oriented snapshot slice — cheap to build, wire-friendly, and
/// what the HTTP walk-backward endpoint actually wants. Maps each output
/// field to a dense `Vec<Option<f64>>` aligned to `SymbolSnapshot::bars`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IndicatorColumnSlice {
    pub spec: IndicatorSpec,
    /// Ordered: `columns[i].0` is the field name,
    /// `columns[i].1[j]` is the value at bar `j`.
    pub columns: Vec<(String, Vec<Option<f64>>)>,
    pub ready_since_t: Option<i64>,
}

/// Immutable snapshot of a `SymbolState` — safe to hand out to readers
/// after the write lock is released.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SymbolSnapshot {
    pub symbol: String,
    pub timeframe: Timeframe,
    /// Confirmed closed bars. `bars.last()` (= `bars[bars.len()-1]`) is the
    /// most recent **closed** bar — same semantics as `[0]` in scripts.
    pub bars: Vec<Bar>,
    pub indicators: HashMap<IndicatorSpec, IndicatorSeriesSlice>,
    /// Currently-forming (not yet confirmed) bar for this timeframe.
    /// Updated on every base-TF bar advance via `Ledger::advance_live`.
    /// `None` until the first base-TF bar arrives after startup.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub live_bar: Option<Bar>,
}

/// One registered indicator attached to a `SymbolState`.
///
/// ## Storage
///
/// Series data is stored **columnar**: one ring buffer per output field
/// keyed by the `&'static str` name taken from
/// [`IndicatorBox::field_names`]. Invariant: every column has length
/// `<= capacity` and all columns are the same length.
///
/// Memory savings over a row-oriented `VecDeque<Option<HashMap>>`:
/// - Scalar indicators (EMA, SMA, RSI): ~11× smaller
/// - Multi-field (MACD 3 fields): ~4× smaller
///
/// See the module-level tests for concrete alignment guarantees.
pub struct IndicatorCell {
    pub spec: IndicatorSpec,
    box_: IndicatorBox,
    /// Static field names taken once at construction; kept for fast
    /// iteration + stable column ordering.
    field_names: &'static [&'static str],
    /// One ring per output field. Order matches `field_names`.
    /// `None` means the indicator was not yet warmed up at that bar.
    columns: Vec<VecDeque<Option<f64>>>,
    /// Timestamp of the first bar where the indicator produced a value.
    pub ready_since_t: Option<i64>,
    /// True when the indicator wants a session-boundary reset (VWAP).
    pub session_aware: bool,
    /// Number of live [`IndicatorHandle`](crate::handle::IndicatorHandle)s
    /// referencing this cell. Pinned cells ignore this value.
    pub refcount: usize,
    /// Pinned cells are never evicted (warm-set lifetime). The refcount
    /// is still maintained in case a client happens to also acquire the
    /// same spec — no harm, just never reaches the grace path.
    pub pinned: bool,
    /// When `refcount` drops to 0 (and not pinned) this is set to the
    /// `advance_counter` value at which the cell becomes eligible for
    /// eviction. Any re-acquire before that clears it and reuses the
    /// warmed state.
    pub pending_drop_at_counter: Option<u64>,
}

impl IndicatorCell {
    pub fn new(spec: IndicatorSpec, capacity: usize) -> anyhow::Result<Self> {
        let session_aware = spec.is_session_aware();
        let box_ = IndicatorBox::from_config(&spec.config)?;
        let field_names = box_.field_names();
        let columns = (0..field_names.len())
            .map(|_| VecDeque::with_capacity(capacity))
            .collect();
        Ok(Self {
            spec,
            box_,
            field_names,
            columns,
            ready_since_t: None,
            session_aware,
            refcount: 0,
            pinned: false,
            pending_drop_at_counter: None,
        })
    }

    /// Field names in storage order. Same `&'static` slice returned by
    /// [`IndicatorBox::field_names`] for the underlying indicator.
    #[inline]
    pub fn field_names(&self) -> &'static [&'static str] {
        self.field_names
    }

    /// Number of rows currently stored (same for every column — invariant).
    #[inline]
    pub fn len(&self) -> usize {
        self.columns.first().map(|c| c.len()).unwrap_or(0)
    }

    #[inline]
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }

    /// Direct read-only access to one column's ring. Returns `None` if
    /// the field name is not produced by this indicator.
    pub fn column(&self, field: &str) -> Option<&VecDeque<Option<f64>>> {
        let idx = self.field_names.iter().position(|f| *f == field)?;
        self.columns.get(idx)
    }

    /// Advance this cell by one bar. Writes every column (Some or None),
    /// enforcing the sliding-window cap so columns stay aligned with
    /// `bar_window`. Returns the row-oriented value for compat with
    /// observers that consume `AdvanceOutcome`.
    pub fn update(&mut self, bar: &Bar, cap: usize) -> Option<IndicatorFields> {
        let out = self.box_.update(bar);
        if out.is_some() && self.ready_since_t.is_none() {
            self.ready_since_t = Some(bar.timestamp);
        }
        // Fan the (possibly None) row into each column.
        for (idx, col) in self.columns.iter_mut().enumerate() {
            let v = out
                .as_ref()
                .and_then(|m| m.get(self.field_names[idx]).copied());
            push_bounded(col, v, cap);
        }
        out
    }

    /// Reset the underlying indicator state (e.g. at a session boundary for VWAP).
    /// The column buffers are kept — only future updates start fresh.
    pub fn reset_indicator(&mut self) {
        self.box_.reset();
    }

    /// Row-oriented materialisation — one `IndicatorFields` (HashMap) per
    /// stored row. Expensive; primarily kept for compatibility with the
    /// legacy `SymbolSnapshot::indicators` view.
    pub fn slice(&self) -> IndicatorSeriesSlice {
        let n = self.len();
        let mut values: Vec<Option<IndicatorFields>> = Vec::with_capacity(n);
        for i in 0..n {
            let mut row = IndicatorFields::with_capacity(self.field_names.len());
            let mut all_some = true;
            for (idx, col) in self.columns.iter().enumerate() {
                match col.get(i).and_then(|o| o.as_ref()) {
                    Some(v) => {
                        row.insert(self.field_names[idx].to_string(), *v);
                    }
                    None => {
                        all_some = false;
                        break;
                    }
                }
            }
            values.push(if all_some { Some(row) } else { None });
        }
        IndicatorSeriesSlice {
            spec: self.spec.clone(),
            values,
            ready_since_t: self.ready_since_t,
        }
    }

    /// Columnar slice — cheap, one `Vec<Option<f64>>` per field.
    /// Preferred wire format for HTTP responses.
    pub fn slice_columnar(&self) -> IndicatorColumnSlice {
        let columns = self
            .columns
            .iter()
            .enumerate()
            .map(|(idx, col)| {
                (self.field_names[idx].to_string(), col.iter().copied().collect())
            })
            .collect();
        IndicatorColumnSlice {
            spec: self.spec.clone(),
            columns,
            ready_since_t: self.ready_since_t,
        }
    }

    /// Current (= most recent) value, if the indicator is warmed up.
    /// Allocates a small `HashMap` (1–3 entries for most indicators).
    pub fn current(&self) -> Option<IndicatorFields> {
        let last = self.len().checked_sub(1)?;
        let mut m = IndicatorFields::with_capacity(self.field_names.len());
        for (idx, col) in self.columns.iter().enumerate() {
            let v = col.get(last).and_then(|o| o.as_ref()).copied()?;
            m.insert(self.field_names[idx].to_string(), v);
        }
        Some(m)
    }
}

/// Sliding window of bars plus registered indicators for one (symbol, tf).
pub struct SymbolState {
    pub symbol: String,
    pub timeframe: Timeframe,
    /// Confirmed closed bars (sliding window, length ≤ `capacity`).
    /// Most recent closed bar = `bar_window.back()`.
    pub bar_window: VecDeque<Bar>,
    pub indicators: HashMap<IndicatorSpec, IndicatorCell>,
    /// Maximum number of bars retained in `bar_window` (and each indicator's series).
    pub capacity: usize,
    /// Timestamp of the most recently advanced bar. `None` before any advance.
    pub last_ts: Option<i64>,
    /// Monotonic count of successful advances. Drives grace-period eviction
    /// of unreferenced indicator cells (see `IndicatorCell::pending_drop_at_counter`).
    /// Not the same as `bar_window.len()` — the window is capped; this is not.
    pub advance_counter: u64,
    /// Currently-forming bar for this timeframe — the incomplete bucket whose
    /// base-TF bars have arrived but whose close has not been confirmed yet.
    /// Set by `advance_live()`; `None` until the first base-TF bar is seen.
    /// Cleared and replaced each time a new HTF bucket opens.
    pub live_bar: Option<Bar>,
}

impl SymbolState {
    pub fn new(symbol: impl Into<String>, timeframe: Timeframe, capacity: usize) -> Self {
        Self {
            symbol: symbol.into(),
            timeframe,
            bar_window: VecDeque::with_capacity(capacity),
            indicators: HashMap::new(),
            capacity,
            last_ts: None,
            advance_counter: 0,
            live_bar: None,
        }
    }

    /// Increment refcount on `spec`'s cell. Returns whether the spec was
    /// known (false means the caller should call `ensure_indicator`).
    /// Clears any pending drop — "re-acquire during grace" path.
    pub(crate) fn retain_indicator(&mut self, spec: &IndicatorSpec) -> bool {
        if let Some(cell) = self.indicators.get_mut(spec) {
            cell.refcount = cell.refcount.saturating_add(1);
            if cell.pending_drop_at_counter.take().is_some() {
                trace!(
                    symbol = %self.symbol,
                    tf = ?self.timeframe,
                    spec = %spec,
                    refcount = cell.refcount,
                    "re-acquired during grace — pending drop cleared",
                );
            }
            true
        } else {
            false
        }
    }

    /// Decrement refcount on `spec`'s cell. When it reaches 0 and the
    /// cell is not pinned, schedule an eviction at
    /// `advance_counter + grace_bars`. No-op if the spec is unknown
    /// (possible if already evicted and a stale handle is draining).
    pub(crate) fn release_indicator(&mut self, spec: &IndicatorSpec, grace_bars: u64) {
        let Some(cell) = self.indicators.get_mut(spec) else {
            trace!(
                symbol = %self.symbol,
                tf = ?self.timeframe,
                spec = %spec,
                "release_indicator: cell not found (already evicted?)",
            );
            return;
        };
        if cell.refcount == 0 {
            trace!(
                symbol = %self.symbol,
                tf = ?self.timeframe,
                spec = %spec,
                "release_indicator: refcount already 0 — ignoring",
            );
            return;
        }
        cell.refcount -= 1;
        if cell.refcount == 0 && !cell.pinned {
            let due = self.advance_counter.saturating_add(grace_bars);
            cell.pending_drop_at_counter = Some(due);
            debug!(
                symbol = %self.symbol,
                tf = ?self.timeframe,
                spec = %spec,
                grace_bars,
                evict_at = due,
                "refcount reached 0 — scheduled grace eviction",
            );
        }
    }

    /// Mark a cell as pinned (immortal). Idempotent.
    pub(crate) fn pin_indicator(&mut self, spec: &IndicatorSpec) {
        if let Some(cell) = self.indicators.get_mut(spec) {
            if !cell.pinned {
                cell.pinned = true;
                cell.pending_drop_at_counter = None;
                debug!(
                    symbol = %self.symbol,
                    tf = ?self.timeframe,
                    spec = %spec,
                    "indicator pinned — will not be evicted",
                );
            }
        }
    }

    /// Evict any cell whose grace period has expired. Called inside
    /// `advance` after the counter is incremented.
    fn sweep_expired(&mut self) {
        if self.indicators.is_empty() {
            return;
        }
        let counter = self.advance_counter;
        let to_remove: Vec<IndicatorSpec> = self
            .indicators
            .iter()
            .filter_map(|(spec, cell)| match cell.pending_drop_at_counter {
                Some(due) if cell.refcount == 0 && !cell.pinned && counter >= due => {
                    Some(spec.clone())
                }
                _ => None,
            })
            .collect();
        for spec in to_remove {
            self.indicators.remove(&spec);
            debug!(
                symbol = %self.symbol,
                tf = ?self.timeframe,
                spec = %spec,
                at_counter = counter,
                "evicted indicator after grace period",
            );
        }
    }

    /// Grow `capacity` to at least `wanted` — idempotent, never shrinks.
    /// Existing bars are kept; the backing VecDeque grows on next insertion.
    pub fn ensure_capacity(&mut self, wanted: usize) {
        if wanted > self.capacity {
            self.capacity = wanted;
        }
    }

    /// Push a new bar and advance all registered indicators.
    /// Returns the list of indicators that produced a fresh value this step.
    pub fn advance(&mut self, bar: Bar) -> AdvanceOutcome {
        let ts = bar.timestamp;

        // Detect duplicate / out-of-order bars. Strategy: ignore anything
        // not strictly newer than the last bar to keep the state monotonic.
        if let Some(last) = self.last_ts {
            if ts <= last {
                debug!(
                    symbol = %self.symbol,
                    tf = ?self.timeframe,
                    last_ts = last,
                    bar_ts = ts,
                    "skipping out-of-order / duplicate bar",
                );
                return AdvanceOutcome { ts, new_values: Vec::new(), skipped: true };
            }
        }

        // Session boundary (daily open, UTC) — reset session-aware indicators.
        let session_changed = is_new_utc_session(self.last_ts, ts);
        if session_changed {
            let mut reset_count = 0usize;
            for cell in self.indicators.values_mut() {
                if cell.session_aware {
                    cell.reset_indicator();
                    reset_count += 1;
                }
            }
            if reset_count > 0 {
                trace!(
                    symbol = %self.symbol,
                    tf = ?self.timeframe,
                    bar_ts = ts,
                    reset_count,
                    "session boundary crossed — session-aware indicators reset",
                );
            }
        }

        self.last_ts = Some(ts);
        self.advance_counter = self.advance_counter.saturating_add(1);
        push_bounded(&mut self.bar_window, bar.clone(), self.capacity);

        // Evict expired cells before updating — their columns won't grow
        // one last time, matching user expectations ("I dropped it N bars
        // ago, it should be gone by now").
        self.sweep_expired();

        let mut new_values: Vec<(IndicatorSpec, IndicatorFields)> =
            Vec::with_capacity(self.indicators.len());
        for cell in self.indicators.values_mut() {
            let was_warm = cell.ready_since_t.is_some();
            if let Some(v) = cell.update(&bar, self.capacity) {
                if !was_warm {
                    // First-ever value for this cell — log at debug once.
                    debug!(
                        symbol = %self.symbol,
                        tf = ?self.timeframe,
                        spec = %cell.spec,
                        ready_at = ts,
                        "indicator reached ready state",
                    );
                }
                new_values.push((cell.spec.clone(), v));
            }
        }

        trace!(
            symbol = %self.symbol,
            tf = ?self.timeframe,
            bar_ts = ts,
            window = self.bar_window.len(),
            indicators = self.indicators.len(),
            produced = new_values.len(),
            "advanced bar",
        );

        AdvanceOutcome { ts, new_values, skipped: false }
    }

    /// Register a new indicator (idempotent). If the spec already exists,
    /// does nothing and returns `false`; otherwise creates the cell and
    /// warms it up by replaying the current `bar_window`.
    pub fn ensure_indicator(&mut self, spec: IndicatorSpec) -> anyhow::Result<bool> {
        if self.indicators.contains_key(&spec) {
            trace!(
                symbol = %self.symbol,
                tf = ?self.timeframe,
                spec = %spec,
                "ensure_indicator hit existing cell — noop",
            );
            return Ok(false);
        }
        let mut cell = IndicatorCell::new(spec.clone(), self.capacity)?;
        // Warm up against existing bar window so the new indicator is
        // immediately aligned with the rest of the state. `bar_window.len()
        // <= capacity`, so `push_bounded` inside `update` never pops here.
        let cap = self.capacity;
        for bar in &self.bar_window {
            cell.update(bar, cap);
        }
        let ready = cell.ready_since_t;
        debug!(
            symbol = %self.symbol,
            tf = ?self.timeframe,
            spec = %spec,
            warmup_bars = self.bar_window.len(),
            ready_since_t = ?ready,
            "registered new indicator",
        );
        self.indicators.insert(spec, cell);
        Ok(true)
    }

    /// Update the forming bar without advancing the confirmed window.
    /// Called on every base-TF bar to keep `live_bar` current.
    /// No observer notification — `live_bar` is a peek, not a confirmed advance.
    #[inline]
    pub fn advance_live(&mut self, bar: Bar) {
        self.live_bar = Some(bar);
    }

    /// Build an immutable snapshot for readers.
    pub fn snapshot(&self) -> SymbolSnapshot {
        SymbolSnapshot {
            symbol: self.symbol.clone(),
            timeframe: self.timeframe,
            bars: self.bar_window.iter().cloned().collect(),
            indicators: self.indicators
                .iter()
                .map(|(spec, cell)| (spec.clone(), cell.slice()))
                .collect(),
            live_bar: self.live_bar.clone(),
        }
    }
}

/// Outcome of a single `advance` call — what the ledger reports back to observers.
#[derive(Debug, Clone)]
pub struct AdvanceOutcome {
    pub ts: i64,
    /// Indicator specs that produced a new value on this bar.
    pub new_values: Vec<(IndicatorSpec, IndicatorFields)>,
    /// True if the bar was ignored (out-of-order / duplicate).
    pub skipped: bool,
}

fn is_new_utc_session(prev_ts: Option<i64>, current_ts: i64) -> bool {
    const DAY_MS: i64 = 86_400_000;
    let prev = match prev_ts {
        Some(p) => p,
        None => return false,
    };
    prev.div_euclid(DAY_MS) != current_ts.div_euclid(DAY_MS)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn mk_bar(t: i64, c: f64) -> Bar {
        Bar::new(t, "TEST", c, c, c, c, 1.0)
    }

    #[test]
    fn advance_pushes_into_window() {
        let mut s = SymbolState::new("BTCUSDT", Timeframe::M1, 10);
        for i in 0..5 {
            s.advance(mk_bar((i + 1) * 60_000, 100.0));
        }
        assert_eq!(s.bar_window.len(), 5);
        assert_eq!(s.last_ts, Some(5 * 60_000));
    }

    #[test]
    fn advance_enforces_capacity() {
        let mut s = SymbolState::new("BTCUSDT", Timeframe::M1, 3);
        for i in 0..10 {
            s.advance(mk_bar((i + 1) * 60_000, 100.0));
        }
        assert_eq!(s.bar_window.len(), 3);
    }

    #[test]
    fn advance_rejects_out_of_order() {
        let mut s = SymbolState::new("BTCUSDT", Timeframe::M1, 10);
        s.advance(mk_bar(2_000, 100.0));
        let out = s.advance(mk_bar(1_000, 99.0));
        assert!(out.skipped);
        assert_eq!(s.bar_window.len(), 1);
    }

    #[test]
    fn ensure_indicator_warms_up_on_existing_bars() {
        let mut s = SymbolState::new("BTCUSDT", Timeframe::M1, 100);
        for i in 0..20 {
            s.advance(mk_bar((i + 1) * 60_000, 100.0 + i as f64));
        }
        let spec = IndicatorSpec::from_config(json!({"type":"ema","period":5}), None).unwrap();
        let created = s.ensure_indicator(spec.clone()).unwrap();
        assert!(created);
        let cell = s.indicators.get(&spec).unwrap();
        assert_eq!(cell.len(), 20);
        assert!(cell.current().is_some(), "EMA(5) should be warm after 20 bars");
    }

    #[test]
    fn ensure_indicator_idempotent() {
        let mut s = SymbolState::new("BTCUSDT", Timeframe::M1, 100);
        let spec = IndicatorSpec::from_config(json!({"type":"ema","period":5}), None).unwrap();
        assert!(s.ensure_indicator(spec.clone()).unwrap());
        assert!(!s.ensure_indicator(spec).unwrap());
        assert_eq!(s.indicators.len(), 1);
    }

    #[test]
    fn advance_drives_indicator_series() {
        let mut s = SymbolState::new("BTCUSDT", Timeframe::M1, 100);
        let spec = IndicatorSpec::from_config(json!({"type":"ema","period":3}), None).unwrap();
        s.ensure_indicator(spec.clone()).unwrap();
        for i in 0..5 {
            s.advance(mk_bar((i + 1) * 60_000, 100.0 + i as f64));
        }
        let cell = s.indicators.get(&spec).unwrap();
        assert_eq!(cell.len(), 5);
        assert!(cell.current().is_some());
    }

    #[test]
    fn columnar_storage_aligned_and_trimmed() {
        // All columns must stay the same length as bar_window, including
        // after capacity eviction.
        let mut s = SymbolState::new("BTCUSDT", Timeframe::M1, 4);
        let spec = IndicatorSpec::from_config(
            json!({"type":"macd","fast":2,"slow":3,"signal":2}),
            None,
        )
        .unwrap();
        s.ensure_indicator(spec.clone()).unwrap();
        for i in 0..10 {
            s.advance(mk_bar((i + 1) * 60_000, 100.0 + i as f64));
        }
        let cell = s.indicators.get(&spec).unwrap();
        assert_eq!(s.bar_window.len(), 4);
        assert_eq!(cell.len(), 4);
        // Every field column must have exactly `capacity` rows.
        for field in cell.field_names() {
            let col = cell.column(field).expect("field must exist");
            assert_eq!(col.len(), 4, "column {} misaligned", field);
        }
    }

    #[test]
    fn columnar_column_lookup_and_ordering() {
        let mut s = SymbolState::new("BTCUSDT", Timeframe::M1, 50);
        let spec = IndicatorSpec::from_config(json!({"type":"ema","period":3}), None).unwrap();
        s.ensure_indicator(spec.clone()).unwrap();
        for i in 0..10 {
            s.advance(mk_bar((i + 1) * 60_000, 100.0 + i as f64));
        }
        let cell = s.indicators.get(&spec).unwrap();
        // EMA is scalar — single "value" field.
        assert_eq!(cell.field_names(), &["value"]);
        let col = cell.column("value").unwrap();
        assert_eq!(col.len(), 10);
        // Unknown field returns None.
        assert!(cell.column("nonexistent").is_none());
        // Row-oriented slice materialisation stays consistent with columnar.
        let slice = cell.slice();
        assert_eq!(slice.values.len(), 10);
        let columnar = cell.slice_columnar();
        assert_eq!(columnar.columns.len(), 1);
        assert_eq!(columnar.columns[0].0, "value");
        assert_eq!(columnar.columns[0].1.len(), 10);
        // Same value at the tip via both views.
        let row_last = slice.values.last().unwrap().as_ref().unwrap();
        let col_last = columnar.columns[0].1.last().unwrap().unwrap();
        assert_eq!(row_last.get("value").copied().unwrap(), col_last);
    }
}
