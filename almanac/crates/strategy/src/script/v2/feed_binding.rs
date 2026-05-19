//! v2 per-indicator binding: confirmed state advanced by HTF *feed* bars
//! (not by resampling); live forming value computed via **clone-and-project**.
//!
//! # Clone-and-project — the v2 live model
//!
//! For an indicator bound to H1, the binding maintains exactly one canonical
//! state (`ind`) advanced only by real H1 feed bars. Whenever the script
//! needs a live "what would EMA be right now?" value:
//!
//! 1. Take the forming H1 bar from the [`LiveBucketAggregator`] (M1 bars
//!    folded into the current bucket).
//! 2. **Clone** `ind` (cheap — `IndicatorBox` derives `Clone`).
//! 3. Feed the forming bar into the clone via `update`.
//! 4. Extract the value and discard the clone.
//!
//! The canonical state is never mutated by intermediate (forming) data —
//! when the real H1 feed bar finally arrives it advances `ind` once,
//! deterministically. No drift, no double-counting, no separate live
//! indicator instance.
//!
//! Compare with v1, which kept two parallel indicator copies (`ind` + `live_ind`)
//! and resampled M1 → HTF internally. v2 has one indicator, one source of
//! HTF truth, and clones on demand.

use std::collections::{HashMap, VecDeque};

use alm_core::{bar::Bar, Timeframe};
use alm_indicator::IndicatorBox;
use rhai::{Array, Dynamic};

use super::live_agg::LiveBucketAggregator;
use crate::script::v1::MEntry;

// ── History storage (mirrors v1::binding::History for now) ──────────────────

/// What the script extracts from each indicator output.
#[derive(Debug, Clone)]
pub(in crate::script) enum FieldExtract {
    /// Single named field (e.g. EMA `"value"`, RSI `"value"`).
    Single(String),
    /// Multi-field map (e.g. MACD, ADX). `primary` is the field used for
    /// direct numeric comparison.
    Multi { primary: String },
}

#[derive(Debug)]
enum History {
    Single { field: String, data: VecDeque<f64> },
    Multi { primary: String, data: VecDeque<HashMap<String, f64>> },
}

impl History {
    fn new(extract: FieldExtract, capacity: usize) -> Self {
        match extract {
            FieldExtract::Single(field) =>
                Self::Single { field, data: VecDeque::with_capacity(capacity) },
            FieldExtract::Multi { primary } =>
                Self::Multi { primary, data: VecDeque::with_capacity(capacity) },
        }
    }

    fn push(&mut self, fields: &HashMap<String, f64>, cap: usize) {
        match self {
            Self::Single { field, data } => {
                if let Some(&v) = fields.get(field.as_str()) {
                    data.push_back(v);
                    if data.len() > cap {
                        data.pop_front();
                    }
                }
            }
            Self::Multi { data, .. } => {
                data.push_back(fields.clone());
                if data.len() > cap {
                    data.pop_front();
                }
            }
        }
    }

    fn len(&self) -> usize {
        match self {
            Self::Single { data, .. } => data.len(),
            Self::Multi { data, .. } => data.len(),
        }
    }
}

// ── FeedVarBinding ──────────────────────────────────────────────────────────

/// One Rhai-visible variable backed by an indicator that listens to a feed.
pub(in crate::script) struct FeedVarBinding {
    /// Canonical indicator state. Advanced *only* by confirmed bars from the
    /// bound feed (real HTF bars when `target_tf` is set, real base bars
    /// otherwise).
    ind: IndicatorBox,
    /// Which TF this binding listens to. `None` = base TF (engine's
    /// configured base_tf — populated externally when applying bars).
    target_tf: Option<Timeframe>,
    /// Confirmed history exposed to the script as an array.
    history: History,
    buf_depth: usize,
    /// Live state — present iff `enable_live` was set at construction time.
    live: Option<LiveState>,
}

struct LiveState {
    agg: LiveBucketAggregator,
    /// Current live scalar (for `FieldExtract::Single`).
    pub(super) val: f64,
    /// Current live field map (for `FieldExtract::Multi`).
    pub(super) map: HashMap<String, f64>,
    /// 0..=1 fraction of the current bucket observed.
    pub(super) fill_ratio: f64,
}

impl FeedVarBinding {
    pub(in crate::script) fn new(
        ind:       IndicatorBox,
        target_tf: Option<Timeframe>,
        extract:   FieldExtract,
        buf_depth: usize,
        enable_live: bool,
        base_tf_ms: i64,
    ) -> anyhow::Result<Self> {
        let live = if enable_live {
            let tf_ms = target_tf
                .map(|t| t.duration_ms())
                .unwrap_or(base_tf_ms);
            Some(LiveState {
                agg: LiveBucketAggregator::new(tf_ms, base_tf_ms)?,
                val: 0.0,
                map: HashMap::new(),
                fill_ratio: 0.0,
            })
        } else {
            None
        };
        Ok(Self {
            ind,
            target_tf,
            history: History::new(extract, buf_depth),
            buf_depth,
            live,
        })
    }

    /// The TF this binding listens to (`None` = base TF).
    pub(in crate::script) fn target_tf(&self) -> Option<Timeframe> {
        self.target_tf
    }

    pub(in crate::script) fn live_val(&self) -> Option<f64> {
        self.live.as_ref().map(|l| l.val)
    }

    pub(in crate::script) fn live_map(&self) -> Option<&HashMap<String, f64>> {
        self.live.as_ref().map(|l| &l.map)
    }

    pub(in crate::script) fn live_fill_ratio(&self) -> Option<f64> {
        self.live.as_ref().map(|l| l.fill_ratio)
    }

    /// Feed a CONFIRMED bar from this binding's bound feed.
    ///
    /// Advances `ind` and pushes the resulting fields to `history`. Also
    /// resets the live aggregator since the bucket the live state was
    /// tracking is now closed for real.
    ///
    /// Returns `true` when the confirmed history buffer has reached its
    /// target depth (script can be invoked safely).
    pub(in crate::script) fn feed_confirmed(&mut self, bar: &Bar) -> bool {
        if let Some(fields) = self.ind.update(bar) {
            self.history.push(&fields, self.buf_depth);
        }
        // The bucket the live aggregator was building is now confirmed —
        // discard partial state so the next base bar starts the next
        // bucket cleanly.
        if let Some(live) = &mut self.live {
            live.agg.reset();
            live.fill_ratio = 0.0;
            live.val = 0.0;
            live.map.clear();
        }
        self.history.len() >= self.buf_depth
    }

    /// Convert confirmed history to a Rhai-compatible array (newest at [0]).
    ///
    /// - `Single` binding → `Array<f64>`
    /// - `Multi` binding  → `Array<MEntry>` (property getters registered in engine)
    pub(in crate::script) fn to_script_array(&self) -> Array {
        match &self.history {
            History::Single { data, .. } =>
                data.iter().rev().map(|&v| Dynamic::from_float(v)).collect(),
            History::Multi { primary, data } =>
                data.iter().rev()
                    .map(|fields| Dynamic::from(MEntry::new(fields.clone(), primary.clone())))
                    .collect(),
        }
    }

    pub(in crate::script) fn confirmed_len(&self) -> usize {
        self.history.len()
    }

    /// True when the history buffer has reached its target depth.
    pub(in crate::script) fn is_warm(&self) -> bool {
        self.history.len() >= self.buf_depth
    }

    pub(in crate::script) fn is_multi(&self) -> bool {
        matches!(self.history, History::Multi { .. })
    }

    /// Primary field name for multi-output indicators (used to build live MEntry).
    pub(in crate::script) fn live_primary(&self) -> Option<&str> {
        match &self.history {
            History::Multi { primary, .. } => Some(primary.as_str()),
            _ => None,
        }
    }

    /// Fold a base bar into the live forming bucket and recompute the live
    /// value via clone-and-project.
    ///
    /// No-op when this binding has live disabled or is itself the base TF
    /// (base-TF bindings derive their value from `feed_confirmed` directly;
    /// "live" for a base-TF indicator equals the confirmed last value).
    pub(in crate::script) fn tick_live(&mut self, base_bar: &Bar) {
        let Some(live) = &mut self.live else { return; };
        // For base-TF bindings, the canonical state IS the live state.
        // We could redundantly mirror it, but the script API surfaces it
        // through the confirmed series already.
        if self.target_tf.is_none() {
            return;
        }
        live.agg.accumulate(base_bar);
        live.fill_ratio = live.agg.fill_ratio();
        let Some(forming) = live.agg.peek() else { return; };
        // Clone-and-project: feed the forming bar to a clone, read out its
        // would-be value, throw the clone away.
        let mut probe = self.ind.clone();
        if let Some(fields) = probe.update(&forming) {
            match &self.history {
                History::Single { field, .. } => {
                    if let Some(&v) = fields.get(field.as_str()) {
                        live.val = v;
                    }
                }
                History::Multi { .. } => {
                    live.map = fields;
                }
            }
        }
    }

    /// Reset all state — indicator, history, and live forming bucket.
    pub(in crate::script) fn reset(&mut self) {
        self.ind.reset();
        match &mut self.history {
            History::Single { data, .. } => data.clear(),
            History::Multi  { data, .. } => data.clear(),
        }
        if let Some(live) = &mut self.live {
            live.agg.reset();
            live.val = 0.0;
            live.map.clear();
            live.fill_ratio = 0.0;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::Bar;
    use alm_indicator::IndicatorBox;
    use serde_json::json;

    fn ema_box(period: usize) -> IndicatorBox {
        IndicatorBox::from_config(&json!({"type":"ema","period":period})).unwrap()
    }

    fn b(t: i64, c: f64) -> Bar {
        Bar::new(t, "T", c, c + 0.1, c - 0.1, c, 10.0)
    }

    // ── feed_confirmed path ──────────────────────────────────────────────────

    #[test]
    fn feed_confirmed_advances_indicator_and_history() {
        let mut bind = FeedVarBinding::new(
            ema_box(3),
            Some(Timeframe::H1),
            FieldExtract::Single("value".into()),
            5,
            false,           // no live
            60_000,
        ).unwrap();
        // EMA(3) seeds after 3 confirmed bars; buf_depth=5 means "ready"
        // requires 5 history entries.
        assert_eq!(bind.confirmed_len(), 0);
        bind.feed_confirmed(&b(0, 100.0));
        bind.feed_confirmed(&b(3_600_000, 110.0));
        let after_3 = bind.feed_confirmed(&b(7_200_000, 120.0));
        // After 3 bars: EMA seeded (1st value pushed). Len = 1 (only the seed
        // produces output; the first two updates return None). Not yet ready.
        // Actually EMA emits on bar 3 (the seed bar) so len = 1.
        assert!(!after_3, "buf_depth=5 not yet reached");
        // Feed enough additional bars to fill the buffer.
        for i in 0..5 {
            bind.feed_confirmed(&b(10_800_000 + i * 3_600_000, 130.0 + i as f64));
        }
        assert_eq!(bind.confirmed_len(), 5, "buffer should cap at 5");
    }

    // ── Clone-and-project: live equals what confirmed WOULD become ──────────

    #[test]
    fn live_equals_what_confirmed_would_become_at_bucket_close() {
        // Setup: H1 EMA(3), feed two confirmed H1 bars, then accumulate 60
        // M1 bars whose forming H1 close is known. The live value should
        // equal what `feed_confirmed(forming_bar)` would produce — but
        // without actually mutating ind.
        let mut bind = FeedVarBinding::new(
            ema_box(3),
            Some(Timeframe::H1),
            FieldExtract::Single("value".into()),
            10,
            true,
            60_000,
        ).unwrap();
        // 3 confirmed H1 bars to seed EMA(3). Closes: 100, 110, 120.
        bind.feed_confirmed(&b(0,           100.0));
        bind.feed_confirmed(&b(3_600_000,   110.0));
        bind.feed_confirmed(&b(7_200_000,   120.0));
        // Confirmed EMA(3) after seed = SMA(100,110,120) = 110.
        // Now accumulate M1 bars in the 3rd H1 bucket (2..3h), close trending up.
        // Final forming close should be ~140.
        for i in 0..30 {
            let m1_ts = 2 * 3_600_000 + i * 60_000;
            let close = 120.0 + i as f64;
            bind.tick_live(&b(m1_ts, close));
        }
        // 31 — wait we did 30 M1 bars in bucket starting at 2h. But the H1
        // bucket "2h..3h" already had 3 confirmed bars seeded above. Those
        // confirmed bars used H1 timestamps 0/1h/2h. The forming bucket NOW
        // is the 3h..4h one if our M1 timestamps fall there.
        // Recompute: M1 at 2h..2h+30min are inside bucket 2h..3h. But that
        // bucket was JUST confirmed (the bar at 2h close 120). So this test
        // is muddled. Let me fix.

        // Reset and redo cleanly.
        let mut bind = FeedVarBinding::new(
            ema_box(3),
            Some(Timeframe::H1),
            FieldExtract::Single("value".into()),
            10,
            true,
            60_000,
        ).unwrap();
        // 3 confirmed H1 bars at hours 0,1,2: closes 100, 110, 120 → seed 110.
        bind.feed_confirmed(&b(0,           100.0));
        bind.feed_confirmed(&b(3_600_000,   110.0));
        bind.feed_confirmed(&b(7_200_000,   120.0));

        // Accumulate M1 bars in bucket 3h..4h.
        // Final close after 30 M1 bars: 130 + 29 = 159.
        for i in 0..30 {
            let m1_ts = 3 * 3_600_000 + i * 60_000;
            let close = 130.0 + i as f64;
            bind.tick_live(&b(m1_ts, close));
        }

        // What WOULD EMA(3) be if the 4th H1 bar closed at 159 right now?
        // EMA(3) after seed: alpha = 2/(3+1) = 0.5.
        // current ema = 110 (seeded). new = 159*0.5 + 110*0.5 = 134.5
        let live = bind.live_val().expect("live enabled");
        assert!((live - 134.5).abs() < 1e-6, "live = {live}, expected 134.5");

        // Crucially: the canonical state was NOT advanced. Confirm by feeding
        // the same 159 bar as a CONFIRMED H1 and reading the new live should
        // match what feed_confirmed produced.
        bind.feed_confirmed(&b(3 * 3_600_000, 159.0));
        // Now bind.ind has actually been advanced. Hist last value should be 134.5
        // too (since we just feed the same close into the real state).
        // Live should now reset (live.val = 0 after feed_confirmed clears).
        assert_eq!(bind.live_val(), Some(0.0));
        assert_eq!(bind.live_fill_ratio(), Some(0.0));
    }

    #[test]
    fn live_disabled_means_tick_live_is_noop() {
        let mut bind = FeedVarBinding::new(
            ema_box(3),
            Some(Timeframe::H1),
            FieldExtract::Single("value".into()),
            5,
            false,
            60_000,
        ).unwrap();
        // No-op: tick_live shouldn't even allocate.
        bind.tick_live(&b(0, 100.0));
        assert!(bind.live_val().is_none());
        assert!(bind.live_fill_ratio().is_none());
    }

    #[test]
    fn live_no_op_for_base_tf_binding() {
        // A base-TF binding (target_tf = None) doesn't need clone-and-project:
        // the canonical state IS the latest value. `tick_live` is a no-op.
        let mut bind = FeedVarBinding::new(
            ema_box(3),
            None,
            FieldExtract::Single("value".into()),
            5,
            true,
            60_000,
        ).unwrap();
        bind.tick_live(&b(0, 100.0));
        // No bucket accumulation happened.
        assert_eq!(bind.live_fill_ratio(), Some(0.0));
        assert_eq!(bind.live_val(), Some(0.0));
    }

    #[test]
    fn confirmed_feed_resets_live_state() {
        let mut bind = FeedVarBinding::new(
            ema_box(3),
            Some(Timeframe::H1),
            FieldExtract::Single("value".into()),
            5,
            true,
            60_000,
        ).unwrap();
        // Seed
        bind.feed_confirmed(&b(0,         100.0));
        bind.feed_confirmed(&b(3_600_000, 110.0));
        bind.feed_confirmed(&b(7_200_000, 120.0));
        // Build live state in bucket 3..4h.
        for i in 0..10 {
            bind.tick_live(&b(3 * 3_600_000 + i * 60_000, 150.0));
        }
        assert!(bind.live_fill_ratio().unwrap() > 0.0);
        // Confirmed H1 bar arrives → live state cleared.
        bind.feed_confirmed(&b(3 * 3_600_000, 150.0));
        assert_eq!(bind.live_fill_ratio(), Some(0.0));
        assert_eq!(bind.live_val(), Some(0.0));
    }

    // ── Multi-field indicator (e.g. MACD) ────────────────────────────────────

    #[test]
    fn live_works_for_multi_field_indicator() {
        let macd = IndicatorBox::from_config(
            &json!({"type":"macd","fast":2,"slow":3,"signal":2})
        ).unwrap();
        let mut bind = FeedVarBinding::new(
            macd,
            Some(Timeframe::H1),
            FieldExtract::Multi { primary: "macd".into() },
            5,
            true,
            60_000,
        ).unwrap();
        // Seed with 3 confirmed H1 bars.
        for i in 0..3 {
            bind.feed_confirmed(&b(i * 3_600_000, 100.0 + i as f64));
        }
        // Now project: accumulate M1 bars in the 4th H1 bucket.
        for i in 0..15 {
            bind.tick_live(&b(3 * 3_600_000 + i * 60_000, 200.0));
        }
        let map = bind.live_map().expect("live map present");
        assert!(map.contains_key("macd"));
    }
}
