//! Layered (two-tier) strategy — a **filter** layer gates a **signal** layer.
//!
//! # Design
//!
//! ```text
//! ┌───────────────────────────────────────────┐
//! │ Filter layer (e.g. trend / HTF regime)    │
//! │   Long  signal → gate OPEN                │
//! │   Close signal → gate CLOSED              │
//! └───────────────────────────────────────────┘
//!          ↓ gate state
//! ┌───────────────────────────────────────────┐
//! │ Signal layer (e.g. RSI mean-rev, MACD …)  │
//! │   Runs every bar (stays warmed up)         │
//! │   Signals relayed only when gate is OPEN   │
//! └───────────────────────────────────────────┘
//! ```
//!
//! **Force-close:** if the gate transitions from OPEN → CLOSED while a position
//! is open, a `Close` signal is emitted immediately and the signal layer is reset.
//!
//! # Factory JSON shape
//!
//! ```json
//! {
//!   "strategy": "layered",
//!   "params": {
//!     "filter": {
//!       "name": "ma_crossover",
//!       "params": { "fast": 50, "slow": 200 }
//!     },
//!     "signal": {
//!       "name": "rsi_mean_rev",
//!       "params": { "period": 14, "oversold": 30, "overbought": 70 }
//!     }
//!   }
//! }
//! ```
//!
//! Any strategy registered in [`crate::factory`] can be used for either layer.

use alm_core::{Bar, Signal, Strategy};
use alm_core::signal::Direction;

/// Two-layer strategy: filter controls the gate; signal layer fires through it.
pub struct LayeredStrategy {
    filter:      Box<dyn Strategy>,
    signal:      Box<dyn Strategy>,
    gate_open:   bool,
    in_position: bool,
}

impl LayeredStrategy {
    pub fn new(filter: Box<dyn Strategy>, signal: Box<dyn Strategy>) -> Self {
        Self { filter, signal, gate_open: false, in_position: false }
    }
}

impl Strategy for LayeredStrategy {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        // ── 1. Update filter, track gate state ───────────────────────────────
        let filter_sigs = self.filter.on_bar(bar);
        let was_open = self.gate_open;
        for sig in &filter_sigs {
            match sig.direction {
                Direction::Long  => self.gate_open = true,
                Direction::Close => self.gate_open = false,
                Direction::Short => self.gate_open = false,
            }
        }

        // ── 2. Gate just closed → force-close open position ──────────────────
        if was_open && !self.gate_open && self.in_position {
            self.in_position = false;
            self.signal.reset();
            return vec![Signal::close(bar.timestamp, bar.symbol.as_str())];
        }

        // ── 3. Run signal layer every bar (keeps indicators warmed up) ────────
        let signal_sigs = self.signal.on_bar(bar);

        // ── 4. Gate closed — discard signal output ────────────────────────────
        if !self.gate_open {
            return vec![];
        }

        // ── 5. Gate open — relay signals, track position ─────────────────────
        let mut result = Vec::new();
        for sig in signal_sigs {
            match sig.direction {
                Direction::Long if !self.in_position => {
                    self.in_position = true;
                    result.push(sig);
                }
                Direction::Close if self.in_position => {
                    self.in_position = false;
                    result.push(sig);
                }
                _ => {}
            }
        }
        result
    }

    fn name(&self) -> &str { "layered" }

    fn reset(&mut self) {
        self.filter.reset();
        self.signal.reset();
        self.gate_open   = false;
        self.in_position = false;
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::Bar;

    fn bar(ts: i64, c: f64) -> Bar {
        Bar::new(ts, "T", c, c * 1.01, c * 0.99, c, 1_000.0)
    }

    /// A stub strategy that fires one Long signal on bar N, then stays silent.
    struct OneShot { fire_at: i64, close_at: Option<i64>, count: i64 }
    impl OneShot {
        fn long(ts: i64) -> Self { Self { fire_at: ts, close_at: None, count: 0 } }
        fn long_then_close(l: i64, c: i64) -> Self { Self { fire_at: l, close_at: Some(c), count: 0 } }
    }
    impl Strategy for OneShot {
        fn on_bar(&mut self, b: &Bar) -> Vec<Signal> {
            self.count += 1;
            if b.timestamp == self.fire_at {
                return vec![Signal::long(b.timestamp, &b.symbol, 1.0)];
            }
            if Some(b.timestamp) == self.close_at {
                return vec![Signal::close(b.timestamp, &b.symbol)];
            }
            vec![]
        }
        fn name(&self) -> &str { "oneshot" }
        fn reset(&mut self) { self.count = 0; }
    }

    #[test]
    fn signal_blocked_when_gate_closed() {
        // Filter never fires → gate always closed → signal layer output ignored.
        let filter = Box::new(OneShot { fire_at: i64::MAX, close_at: None, count: 0 });
        let signal = Box::new(OneShot::long(1));
        let mut strat = LayeredStrategy::new(filter, signal);

        let sigs = strat.on_bar(&bar(1, 100.0));
        assert!(sigs.is_empty(), "gate closed — no signal should pass through");
    }

    #[test]
    fn signal_relayed_when_gate_open() {
        // Filter fires Long on bar 1 (gate opens); signal fires Long on bar 2.
        let filter = Box::new(OneShot::long(1));
        let signal = Box::new(OneShot::long(2));
        let mut strat = LayeredStrategy::new(filter, signal);

        let s1 = strat.on_bar(&bar(1, 100.0)); // filter opens gate
        assert!(s1.is_empty(), "filter opened gate; no signal from signal layer yet");

        let s2 = strat.on_bar(&bar(2, 101.0)); // signal fires, gate is open
        assert_eq!(s2.len(), 1);
        assert_eq!(s2[0].direction, Direction::Long);
    }

    #[test]
    fn force_close_when_gate_closes() {
        // Filter: Long on bar 1, Close on bar 5.
        // Signal: Long on bar 2.
        // Expect: force-close emitted on bar 5.
        let filter = Box::new(OneShot::long_then_close(1, 5));
        let signal = Box::new(OneShot::long(2));
        let mut strat = LayeredStrategy::new(filter, signal);

        strat.on_bar(&bar(1, 100.0)); // gate opens
        strat.on_bar(&bar(2, 101.0)); // signal Long
        strat.on_bar(&bar(3, 102.0));
        strat.on_bar(&bar(4, 103.0));

        let s5 = strat.on_bar(&bar(5, 104.0)); // gate closes
        assert_eq!(s5.len(), 1);
        assert_eq!(s5[0].direction, Direction::Close, "force-close when gate closes");
    }

    #[test]
    fn no_double_entry() {
        // Gate open, signal fires Long every bar — only the first should be relayed.
        struct AlwaysLong;
        impl Strategy for AlwaysLong {
            fn on_bar(&mut self, b: &Bar) -> Vec<Signal> {
                vec![Signal::long(b.timestamp, &b.symbol, 1.0)]
            }
            fn name(&self) -> &str { "always_long" }
            fn reset(&mut self) {}
        }

        let filter = Box::new(OneShot::long(1)); // gate opens at bar 1
        let signal = Box::new(AlwaysLong);
        let mut strat = LayeredStrategy::new(filter, signal);

        // Bar 1: filter opens gate AND signal fires → Long relayed
        let s1 = strat.on_bar(&bar(1, 100.0));
        // Bars 2,3: already in position → signal Long is blocked
        let s2 = strat.on_bar(&bar(2, 101.0));
        let s3 = strat.on_bar(&bar(3, 102.0));

        assert_eq!(s1.len(), 1, "first Long when gate opens");
        assert!(s2.is_empty(), "no duplicate Long when already in position");
        assert!(s3.is_empty(), "still blocked");
    }

    #[test]
    fn reset_clears_state() {
        let filter = Box::new(OneShot::long(1));
        let signal = Box::new(OneShot::long(2));
        let mut strat = LayeredStrategy::new(filter, signal);

        strat.on_bar(&bar(1, 100.0));
        strat.on_bar(&bar(2, 101.0));
        assert!(strat.in_position);
        assert!(strat.gate_open);

        strat.reset();
        assert!(!strat.in_position);
        assert!(!strat.gate_open);
    }
}
