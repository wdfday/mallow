//! `SymbolState` — per-(symbol, timeframe) sliding bar window.

use std::collections::VecDeque;

use alm_core::{Bar, Timeframe};
use tracing::trace;

/// Push `v` onto `buf`, then pop from the front until `buf.len() <= cap`.
#[inline]
fn push_bounded<T>(buf: &mut VecDeque<T>, v: T, cap: usize) {
    buf.push_back(v);
    while buf.len() > cap {
        buf.pop_front();
    }
}

/// Outcome of a single `advance` call — reported to observers.
#[derive(Debug, Clone)]
pub struct AdvanceOutcome {
    /// Timestamp of the bar that was just confirmed (bar open, ms).
    pub ts: i64,
    /// True if the bar was ignored (out-of-order / duplicate).
    pub skipped: bool,
}

/// Sliding window of confirmed bars for one `(symbol, timeframe)`.
pub struct SymbolState {
    pub symbol: String,
    pub timeframe: Timeframe,
    /// Confirmed closed bars (sliding window, length ≤ `capacity`).
    /// Most recent closed bar = `bar_window.back()`.
    pub bar_window: VecDeque<Bar>,
    /// Maximum number of bars retained in `bar_window`.
    pub capacity: usize,
    /// Timestamp of the most recently advanced bar. `None` before any advance.
    pub last_ts: Option<i64>,
    /// Currently-forming bar for this timeframe — the incomplete bucket whose
    /// base-TF bars have arrived but whose close has not been confirmed yet.
    /// Set by `advance_live()`; `None` until the first base-TF bar is seen.
    pub live_bar: Option<Bar>,
}

impl SymbolState {
    pub fn new(symbol: impl Into<String>, timeframe: Timeframe, capacity: usize) -> Self {
        Self {
            symbol: symbol.into(),
            timeframe,
            bar_window: VecDeque::with_capacity(capacity),
            capacity,
            last_ts: None,
            live_bar: None,
        }
    }

    /// Grow `capacity` to at least `wanted` — idempotent, never shrinks.
    pub fn ensure_capacity(&mut self, wanted: usize) {
        if wanted > self.capacity {
            self.capacity = wanted;
        }
    }

    /// Push a new bar into the sliding window.
    /// Returns an outcome with `skipped = true` for duplicate / out-of-order bars.
    pub fn advance(&mut self, bar: Bar) -> AdvanceOutcome {
        let ts = bar.timestamp;

        // Reject anything not strictly newer than the last confirmed bar.
        if let Some(last) = self.last_ts {
            if ts <= last {
                trace!(
                    symbol = %self.symbol,
                    tf = ?self.timeframe,
                    bar_ts = ts,
                    last_ts = last,
                    "advance: bar out-of-order or duplicate — skipped",
                );
                return AdvanceOutcome { ts, skipped: true };
            }
        }

        self.last_ts = Some(ts);
        push_bounded(&mut self.bar_window, bar, self.capacity);

        trace!(
            symbol = %self.symbol,
            tf = ?self.timeframe,
            bar_ts = ts,
            window = self.bar_window.len(),
            "advanced bar",
        );

        AdvanceOutcome { ts, skipped: false }
    }

    /// Update the forming bar without advancing the confirmed window.
    /// Called on every base-TF bar to keep `live_bar` current.
    #[inline]
    pub fn advance_live(&mut self, bar: Bar) {
        self.live_bar = Some(bar);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

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
    fn advance_rejects_duplicate_ts() {
        let mut s = SymbolState::new("BTCUSDT", Timeframe::M1, 10);
        s.advance(mk_bar(60_000, 100.0));
        let out = s.advance(mk_bar(60_000, 101.0));
        assert!(out.skipped);
        assert_eq!(s.bar_window.len(), 1);
    }

    #[test]
    fn advance_live_updates_live_bar() {
        let mut s = SymbolState::new("BTCUSDT", Timeframe::M1, 10);
        assert!(s.live_bar.is_none());
        s.advance_live(mk_bar(60_000, 42.0));
        assert_eq!(s.live_bar.map(|b| b.close), Some(42.0));
    }
}
