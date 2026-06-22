//! Multi-timeframe strategy contract.
//!
//! Defines the callback interface and snapshot types used by
//! [`alm_engine::MtfEngine`]. Placed in `alm-core` so that strategy
//! implementations (`alm-strategy`) can implement the trait without a
//! circular dependency on the engine crate.

use std::collections::{HashMap, VecDeque};

use crate::{bar::Bar, signal::Signal, timeframe::Timeframe};

// ── Snapshot view types ───────────────────────────────────────────────────────

/// One TF's confirmed bar window, passed inside [`MtfSnapshot`].
pub struct TfView<'a> {
    pub tf: Timeframe,
    /// Confirmed bars. `back()` is the most-recent closed bar; `front()` is
    /// the oldest inside the window.
    pub confirmed: &'a VecDeque<Bar>,
}

/// One bar that just closed at the current `close_ts` boundary.
pub struct TfBarEvent<'a> {
    pub tf: Timeframe,
    pub bar: &'a Bar,
}

/// Snapshot handed to [`MtfStrategy::on_bars`] at each `close_ts` boundary.
///
/// `events` lists every TF that closed *at this tick* (sorted largest TF
/// first — same as the heap tie-breaker). `views` exposes confirmed history
/// for every registered TF since engine start.
pub struct MtfSnapshot<'a> {
    pub base_tf: Timeframe,
    /// Physical close time (`bar.timestamp + tf.duration_ms()`) shared by
    /// every entry in `events`.
    pub close_ts: i64,
    /// Bars that closed at this tick. Sorted largest TF first.
    pub events: &'a [TfBarEvent<'a>],
    /// Per-TF rolling window of confirmed bars. Reflects state AFTER the
    /// bars in `events` have been pushed.
    pub views: &'a HashMap<Timeframe, TfView<'a>>,
}

impl<'a> MtfSnapshot<'a> {
    /// Rolling window of confirmed base-TF bars.
    /// `back()` is the most recent base bar.
    pub fn base_window(&self) -> Option<&'a VecDeque<Bar>> {
        self.views.get(&self.base_tf).map(|v| v.confirmed)
    }

    /// True when the base TF closed at this tick.
    pub fn base_closed_this_tick(&self) -> bool {
        self.events.iter().any(|e| e.tf == self.base_tf)
    }

    /// The base bar that closed at this tick, if any.
    pub fn base_bar(&self) -> Option<&'a Bar> {
        self.events.iter().find(|e| e.tf == self.base_tf).map(|e| e.bar)
    }

    /// True when `tf` just closed at this tick.
    pub fn just_closed(&self, tf: Timeframe) -> bool {
        self.events.iter().any(|e| e.tf == tf)
    }

    /// Confirmed bars for `tf`, if it was registered.
    pub fn get(&self, tf: Timeframe) -> Option<&'a VecDeque<Bar>> {
        self.views.get(&tf).map(|v| v.confirmed)
    }
}

// ── MtfStrategy trait ─────────────────────────────────────────────────────────

/// Strategy interface for [`alm_engine::MtfEngine`].
///
/// Called once per `close_ts` boundary when one or more bars close
/// simultaneously. Implement this to define multi-timeframe entry/exit logic.
pub trait MtfStrategy: Send {
    /// Process a new set of multi-timeframe bars and return zero or more signals.
    ///
    /// # Design Note: `Vec<Signal>` vs `Option<Signal>`
    /// Returning `Vec<Signal>` is essential for MTF (Multi-Timeframe) and multi-symbol
    /// contexts where multiple timeframe boundaries might align, requiring signals
    /// to be emitted for different intervals or assets simultaneously.
    /// Refer to [`Strategy::on_bar`](crate::strategy::Strategy::on_bar) for a detailed
    /// explanation of performance trade-offs regarding heap allocations.
    fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal>;
    fn name(&self) -> &str;
    fn reset(&mut self);
    /// Optional: return the current regime state for tagging trades.
    fn current_regime(&self) -> Option<&crate::regime::RegimeState> { None }
    /// Optional: return the canonical Rhai v2 script that mirrors this strategy's logic.
    ///
    /// Used by tooling (catalog, docs, IDE) to display the script source.
    /// Returns `None` for strategies that have no script equivalent.
    fn script(&self) -> Option<&'static str> { None }
}

impl MtfStrategy for Box<dyn MtfStrategy> {
    fn on_bars(&mut self, snap: MtfSnapshot<'_>) -> Vec<Signal> { (**self).on_bars(snap) }
    fn name(&self) -> &str { (**self).name() }
    fn reset(&mut self) { (**self).reset() }
    fn current_regime(&self) -> Option<&crate::regime::RegimeState> { (**self).current_regime() }
    fn script(&self) -> Option<&'static str> { (**self).script() }
}
