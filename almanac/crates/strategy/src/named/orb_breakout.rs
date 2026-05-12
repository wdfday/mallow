use alm_core::{bar::Bar, signal::Signal, strategy::Strategy};

/// Opening Range Breakout (ORB) strategy.
///
/// Establishes the opening range from the first `range_bars` bars of each session,
/// then trades breakouts beyond that range.
///
/// - **Long** when close crosses above the opening range high.
/// - **Close** when close falls back below the opening range low (stop-out).
/// - One trade per session maximum; state resets on each new session.
///
/// Session boundary detection: a gap of > `session_gap_mins` between consecutive
/// bars is treated as the start of a new session. Works correctly when combined
/// with `market_hours_only=true` in the backtest request (no need for timezone logic
/// here — the inter-session gaps are always large after market-hours filtering).
///
/// # Params
/// | Key | Default | Description |
/// |-----|---------|-------------|
/// | `range_bars` | 30 | Bars used to form the opening range (30 = first 30 min on 1-min data) |
/// | `session_gap_mins` | 60 | Minutes gap between bars that signals a new session |
pub struct OrbBreakout {
    range_bars: usize,
    session_gap_ms: i64,

    // ── per-session state ────────────────────────────────────────────────────
    bars_in_session: usize,
    or_high: f64,
    or_low: f64,
    range_ready: bool,
    last_ts: Option<i64>,

    // ── stored for reset ─────────────────────────────────────────────────────
    range_bars_cfg: usize,
    session_gap_mins_cfg: usize,
}

impl OrbBreakout {
    pub fn new(range_bars: usize, session_gap_mins: usize) -> Self {
        let gap_ms = (session_gap_mins as i64) * 60 * 1_000;
        Self {
            range_bars,
            session_gap_ms: gap_ms,
            bars_in_session: 0,
            or_high: f64::NEG_INFINITY,
            or_low: f64::INFINITY,
            range_ready: false,
            last_ts: None,
            range_bars_cfg: range_bars,
            session_gap_mins_cfg: session_gap_mins,
        }
    }

    fn reset_session(&mut self) {
        self.bars_in_session = 0;
        self.or_high = f64::NEG_INFINITY;
        self.or_low = f64::INFINITY;
        self.range_ready = false;
    }
}

impl Strategy for OrbBreakout {
    fn on_bar(&mut self, bar: &Bar) -> Vec<Signal> {
        // ── detect new session ───────────────────────────────────────────────
        let is_new_session = match self.last_ts {
            None => true,
            Some(last) => (bar.timestamp - last) > self.session_gap_ms,
        };

        if is_new_session {
            self.reset_session();
        }
        self.last_ts = Some(bar.timestamp);

        // ── build opening range ──────────────────────────────────────────────
        if !self.range_ready {
            self.bars_in_session += 1;
            if bar.high > self.or_high { self.or_high = bar.high; }
            if bar.low  < self.or_low  { self.or_low  = bar.low;  }

            if self.bars_in_session >= self.range_bars {
                self.range_ready = true;
            }
            return vec![];
        }

        // ── signal generation ────────────────────────────────────────────────
        // Long breakout: close crosses above opening range high
        if bar.close > self.or_high {
            let strength = ((bar.close - self.or_high) / self.or_high).clamp(0.0, 1.0);
            return vec![Signal::long(bar.timestamp, &bar.symbol, strength)];
        }

        // Stop-out: close falls below opening range low while in position
        if bar.close < self.or_low {
            return vec![Signal::close(bar.timestamp, &bar.symbol)];
        }

        vec![]
    }

    fn name(&self) -> &str {
        "orb_breakout"
    }

    fn reset(&mut self) {
        self.reset_session();
        self.last_ts = None;
        self.range_bars = self.range_bars_cfg;
        self.session_gap_ms = (self.session_gap_mins_cfg as i64) * 60 * 1_000;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn bar(ts: i64, o: f64, h: f64, l: f64, c: f64) -> Bar {
        Bar::new(ts, "TEST", o, h, l, c, 1000.0)
    }

    const MIN: i64 = 60_000;
    const SESSION_GAP: i64 = 120 * MIN; // 2h gap = new session

    #[test]
    fn test_no_signal_during_opening_range() {
        let mut s = OrbBreakout::new(3, 60);
        // 3 bars to form opening range — no signals yet
        assert!(s.on_bar(&bar(0,       100.0, 105.0, 98.0,  102.0)).is_empty());
        assert!(s.on_bar(&bar(1*MIN,   102.0, 107.0, 100.0, 104.0)).is_empty());
        assert!(s.on_bar(&bar(2*MIN,   104.0, 108.0, 101.0, 106.0)).is_empty());
    }

    #[test]
    fn test_long_on_breakout_above_or_high() {
        let mut s = OrbBreakout::new(3, 60);
        // Build range: high=108, low=98
        s.on_bar(&bar(0,     100.0, 105.0, 98.0,  102.0));
        s.on_bar(&bar(1*MIN, 102.0, 107.0, 100.0, 104.0));
        s.on_bar(&bar(2*MIN, 104.0, 108.0, 101.0, 106.0));

        // Breakout above 108
        let sigs = s.on_bar(&bar(3*MIN, 106.0, 115.0, 105.0, 112.0));
        assert_eq!(sigs.len(), 1);
        use alm_core::signal::Direction;
        assert_eq!(sigs[0].direction, Direction::Long);
    }

    #[test]
    fn test_close_on_stop_out() {
        let mut s = OrbBreakout::new(3, 60);
        s.on_bar(&bar(0,     100.0, 105.0, 98.0,  102.0));
        s.on_bar(&bar(1*MIN, 102.0, 107.0, 100.0, 104.0));
        s.on_bar(&bar(2*MIN, 104.0, 108.0, 101.0, 106.0));
        // Enter long
        s.on_bar(&bar(3*MIN, 106.0, 115.0, 105.0, 112.0));
        // Drop below or_low (98)
        let sigs = s.on_bar(&bar(4*MIN, 100.0, 101.0, 95.0, 96.0));
        assert_eq!(sigs.len(), 1);
        use alm_core::signal::Direction;
        assert_eq!(sigs[0].direction, Direction::Close);
    }

    #[test]
    fn test_new_session_resets_state() {
        let mut s = OrbBreakout::new(3, 60);
        s.on_bar(&bar(0,     100.0, 105.0, 98.0,  102.0));
        s.on_bar(&bar(1*MIN, 102.0, 107.0, 100.0, 104.0));
        s.on_bar(&bar(2*MIN, 104.0, 108.0, 101.0, 106.0));
        s.on_bar(&bar(3*MIN, 106.0, 115.0, 105.0, 112.0)); // long entered

        // New session (gap > 60 min)
        let ts2 = 3*MIN + SESSION_GAP;
        // Should reset — first bar of new session, no signal
        let sigs = s.on_bar(&bar(ts2, 110.0, 115.0, 108.0, 113.0));
        assert!(sigs.is_empty(), "new session should reset, no signal on first bar");
    }
}
