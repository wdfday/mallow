/// Why a trade was closed.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ExitReason {
    /// Closed by strategy signal (`Direction::Exit`).
    Signal,
    /// Stop-loss level hit (fixed % or signal-level absolute price).
    StopLoss,
    /// Take-profit level hit (fixed % or signal-level absolute price).
    TakeProfit,
    /// Trailing stop level hit.
    TrailingStop,
    /// Time-based exit: position held for `max_bars_held` bars.
    MaxBarsHeld,
    /// Force-closed at end of data (backtest termination).
    EndOfData,
}

impl std::fmt::Display for ExitReason {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let s = match self {
            Self::Signal       => "signal",
            Self::StopLoss     => "stop_loss",
            Self::TakeProfit   => "take_profit",
            Self::TrailingStop => "trailing_stop",
            Self::MaxBarsHeld  => "max_bars",
            Self::EndOfData    => "end_of_data",
        };
        f.write_str(s)
    }
}

/// How the engine uses OHLC data to check exit levels within a bar.
#[derive(Debug, Clone, Copy, Default, PartialEq)]
pub enum IntraBarMode {
    /// Check stop against bar.low (long) or bar.high (short).
    /// Check TP against bar.high (long) or bar.low (short).
    /// Fill at the exact stop/target level. Always pessimistic —
    /// if both SL and TP could fire in the same bar, SL fires first.
    #[default]
    Pessimistic,
    /// Same intra-bar checks as `Pessimistic`, but uses bar direction
    /// (close vs open) to determine which extreme happened first.
    /// Up-bar (close > open): low before high → SL checked first for long.
    /// Down-bar (close < open): high before low → TP checked first for long.
    /// It feel like never. So, Pessimistic, or will be Look Intra-Bar.
    OhlcHeuristic,
}

/// Per-position exit state tracked by the engine.
///
/// Exit levels travel with each [`Signal`](crate::signal::Signal): absolute
/// `stop_price` / `target_price`, plus a `trailing_stop_pct` and a
/// `max_bars_held` time-exit. The engine evaluates them every bar while the
/// position is open, in order: trailing-stop → signal stop → signal target →
/// max-bars. The first rule that fires closes the position at the computed
/// fill price. `IntraBarMode` only controls *how* the bar's OHLC is read.
#[derive(Debug, Clone)]
pub struct PositionTracker {
    /// Fill price of the entry order.
    pub entry_price: f64,
    /// Number of bars the position has been open.
    pub bars_held: usize,
    /// Absolute stop-loss price (from `signal.stop_price`).
    pub stop_price: Option<f64>,
    /// Absolute take-profit price (from `signal.target_price`).
    pub target_price: Option<f64>,
    /// Trailing-stop fraction from the running extremum (from `signal.trailing_stop_pct`).
    pub trailing_stop_pct: Option<f64>,
    /// Time-based exit after this many bars (from `signal.max_bars_held`).
    pub max_bars_held: Option<usize>,
    /// Whether this is a long position (true) or short (false).
    pub is_long: bool,
    /// Running extremum for trailing stop.
    /// Long: highest close seen; Short: lowest close seen.
    pub extreme: f64,
    /// Max adverse excursion as a fraction of entry price (always >= 0).
    pub mae: f64,
    /// Max favorable excursion as a fraction of entry price (always >= 0).
    pub mfe: f64,
    /// Pyramiding: number of accumulated entry legs (1 = single entry).
    pub legs: usize,
    /// Pyramiding: fill price of the most recent leg — used to gate the next
    /// add ("only add when price advanced beyond the last leg").
    pub last_entry_price: f64,
}

impl PositionTracker {
    pub fn new(entry_price: f64, is_long: bool) -> Self {
        Self {
            entry_price,
            bars_held: 0,
            stop_price: None,
            target_price: None,
            trailing_stop_pct: None,
            max_bars_held: None,
            is_long,
            extreme: entry_price,
            mae: 0.0,
            mfe: 0.0,
            legs: 1,
            last_entry_price: entry_price,
        }
    }

    /// Construct with pre-computed absolute stop/target levels (ATR-based) plus
    /// optional trailing-stop and time-exit carried by the signal.
    pub fn with_levels(
        entry_price: f64,
        stop_price: Option<f64>,
        target_price: Option<f64>,
        trailing_stop_pct: Option<f64>,
        max_bars_held: Option<usize>,
        is_long: bool,
    ) -> Self {
        Self {
            entry_price,
            bars_held: 0,
            stop_price,
            target_price,
            trailing_stop_pct,
            max_bars_held,
            is_long,
            extreme: entry_price,
            mae: 0.0,
            mfe: 0.0,
            legs: 1,
            last_entry_price: entry_price,
        }
    }

    /// Pyramiding: register an additional leg at `entry_price`, re-basing the exit
    /// levels to the new signal (TP/SL "follow" the latest entry, so the stop
    /// ratchets up as the position pyramids). Bumps the leg count and resets the
    /// trailing extremum + bar counter to the new entry. MAE/MFE are preserved.
    pub fn add_leg(
        &mut self,
        entry_price: f64,
        stop_price: Option<f64>,
        target_price: Option<f64>,
        trailing_stop_pct: Option<f64>,
        max_bars_held: Option<usize>,
    ) {
        self.legs += 1;
        self.last_entry_price = entry_price;
        self.entry_price = entry_price;
        self.stop_price = stop_price;
        self.target_price = target_price;
        self.trailing_stop_pct = trailing_stop_pct;
        self.max_bars_held = max_bars_held;
        self.bars_held = 0;
        self.extreme = entry_price;
    }

    /// Update tracker for the current bar. Returns `Some((fill_price, reason))` if an
    /// exit rule fires, `None` otherwise.
    ///
    /// `fill_price` is the price at which the position should be closed:
    /// - `Pessimistic` / `OhlcHeuristic`: the exact stop/target level breached.
    ///
    /// Time-based exits always fill at `close` regardless of mode.
    pub fn update_and_check(
        &mut self,
        open: f64,
        high: f64,
        low: f64,
        close: f64,
        intra_bar_mode: IntraBarMode,
    ) -> Option<(f64, ExitReason)> {
        self.bars_held += 1;

        // Update MAE/MFE using intra-bar high/low.
        if self.entry_price > f64::EPSILON {
            if self.is_long {
                let adv = (self.entry_price - low) / self.entry_price;
                let fav = (high - self.entry_price) / self.entry_price;
                if adv > self.mae { self.mae = adv; }
                if fav > self.mfe { self.mfe = fav; }
            } else {
                let adv = (high - self.entry_price) / self.entry_price;
                let fav = (self.entry_price - low) / self.entry_price;
                if adv > self.mae { self.mae = adv; }
                if fav > self.mfe { self.mfe = fav; }
            }
        }

        // Update running extremum for trailing stop
        if self.is_long {
            if high > self.extreme { self.extreme = high; }
        } else if low < self.extreme {
            self.extreme = low;
        }

        // Determine which prices to use for stop and target checks.
        // For long: stop fires on low, target fires on high.
        // For short: stop fires on high, target fires on low.
        // Both modes use the same intra-bar extremes; OhlcHeuristic only
        // differs in which fires first when both levels are breached.
        let (stop_check, target_check) =
            if self.is_long { (low, high) } else { (high, low) };

        // Whether to check TP before stop this bar (OhlcHeuristic only).
        //
        // OHLC heuristic: within an up-bar (close > open), price likely went down
        // first (touched low) then up (touched high); inside a down-bar the order
        // reverses. From that we infer which extreme is hit FIRST and therefore
        // which exit rule fires:
        //
        // | Direction | Bar      | First touched | Fires first |
        // |-----------|----------|---------------|-------------|
        // | LONG      | up-bar   | low           | SL (below)  |
        // | LONG      | down-bar | high          | TP (above)  |
        // | SHORT     | up-bar   | low           | TP (below)  |
        // | SHORT     | down-bar | high          | SL (above)  |
        //
        // → `tp_first` = (LONG ∧ down-bar) ∨ (SHORT ∧ up-bar)
        let tp_first = matches!(intra_bar_mode, IntraBarMode::OhlcHeuristic)
            && (
                (self.is_long  && close < open)   // long  + down-bar → TP first
                || (!self.is_long && close > open) // short + up-bar   → TP first
            );

        // Trailing stop level (stop-side), from the running extremum.
        let trail_stop_price = self.trailing_stop_pct.map(|t| {
            if self.is_long { self.extreme * (1.0 - t) } else { self.extreme * (1.0 + t) }
        });

        // Signal-level absolute stop / target prices.
        let sig_stop   = self.stop_price;
        let sig_target = self.target_price;

        // Check a stop-side level (trailing / signal stop).
        let check_stop = |level: f64| -> bool {
            if self.is_long { stop_check <= level } else { stop_check >= level }
        };

        // Check a target-side level (signal target).
        let check_target = |level: f64| -> bool {
            if self.is_long { target_check >= level } else { target_check <= level }
        };

        // Evaluate stops and targets, respecting tp_first ordering.
        let stops: &[Option<(f64, ExitReason)>] = &[
            trail_stop_price.map(|p| (p, ExitReason::TrailingStop)),
            sig_stop.map(|p| (p, ExitReason::StopLoss)),
        ];
        let targets: &[Option<(f64, ExitReason)>] = &[
            sig_target.map(|p| (p, ExitReason::TakeProfit)),
        ];

        let find_stop   = || stops.iter().flatten().find(|(p, _)| check_stop(*p)).map(|(p, r)| (*p, r.clone()));
        let find_target = || targets.iter().flatten().find(|(p, _)| check_target(*p)).map(|(p, r)| (*p, r.clone()));

        let triggered = if tp_first {
            find_target().or_else(find_stop)
        } else {
            find_stop().or_else(find_target)
        };

        if let Some((level_price, reason)) = triggered {
            return Some((level_price, reason));
        }

        // Time-based (always fills at close, no intra-bar check needed).
        if let Some(max) = self.max_bars_held {
            if self.bars_held >= max { return Some((close, ExitReason::MaxBarsHeld)); }
        }

        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Tracker with absolute signal-level SL/TP and OHLC-heuristic intra-bar mode.
    fn tracker_ohlc(entry: f64, sl: f64, tp: f64, is_long: bool) -> PositionTracker {
        PositionTracker::with_levels(entry, Some(sl), Some(tp), None, None, is_long)
    }

    #[test]
    fn ohlc_heuristic_long_upbar_fires_sl_first() {
        // LONG entry at 100. Up-bar (close > open) → low first → SL should fire,
        // even though both 95 (SL) and 110 (TP) are reachable in the bar.
        let mut tr = tracker_ohlc(100.0, 95.0, 110.0, true);
        // Bar: open=99 (already below entry), high=112, low=94, close=108 → up-bar
        let res = tr.update_and_check(99.0, 112.0, 94.0, 108.0, IntraBarMode::OhlcHeuristic);
        let (price, reason) = res.expect("an exit must fire");
        assert_eq!(reason, ExitReason::StopLoss);
        assert!((price - 95.0).abs() < 1e-9, "fill at SL level, got {price}");
    }

    #[test]
    fn ohlc_heuristic_long_downbar_fires_tp_first() {
        // LONG entry at 100. Down-bar → high first → TP fires before SL.
        let mut tr = tracker_ohlc(100.0, 95.0, 110.0, true);
        // Bar: open=109, high=112, low=94, close=98 → down-bar
        let res = tr.update_and_check(109.0, 112.0, 94.0, 98.0, IntraBarMode::OhlcHeuristic);
        let (price, reason) = res.expect("an exit must fire");
        assert_eq!(reason, ExitReason::TakeProfit);
        assert!((price - 110.0).abs() < 1e-9);
    }

    #[test]
    fn ohlc_heuristic_short_upbar_fires_tp_first() {
        // SHORT entry at 100. Up-bar → low first → TP (lower) fires first.
        // SL for short = 105 ; TP = 90
        let mut tr = tracker_ohlc(100.0, 105.0, 90.0, false);
        // Bar: open=101, high=107, low=88, close=104 → up-bar
        let res = tr.update_and_check(101.0, 107.0, 88.0, 104.0, IntraBarMode::OhlcHeuristic);
        let (price, reason) = res.expect("an exit must fire");
        assert_eq!(reason, ExitReason::TakeProfit);
        assert!((price - 90.0).abs() < 1e-9);
    }

    #[test]
    fn ohlc_heuristic_short_downbar_fires_sl_first() {
        // SHORT entry at 100. Down-bar → high first → SL (higher) fires first.
        let mut tr = tracker_ohlc(100.0, 105.0, 90.0, false);
        // Bar: open=102, high=107, low=88, close=96 → down-bar
        let res = tr.update_and_check(102.0, 107.0, 88.0, 96.0, IntraBarMode::OhlcHeuristic);
        let (price, reason) = res.expect("an exit must fire");
        assert_eq!(reason, ExitReason::StopLoss);
        assert!((price - 105.0).abs() < 1e-9);
    }

    #[test]
    fn trailing_stop_long_fires_after_retrace() {
        // LONG entry 100, trail 10%. Price rises to 120 (extreme), then retraces.
        let mut tr = PositionTracker::with_levels(100.0, None, None, Some(0.10), None, true);
        // Bar 1: close 120 → extreme=120, trail level = 108, low 110 → no exit.
        assert!(tr.update_and_check(100.0, 121.0, 110.0, 120.0, IntraBarMode::Pessimistic).is_none());
        // Bar 2: low 107 breaches trail level 108 → trailing stop fires at 108.
        let (price, reason) = tr
            .update_and_check(119.0, 119.0, 107.0, 112.0, IntraBarMode::Pessimistic)
            .expect("trailing stop must fire");
        assert_eq!(reason, ExitReason::TrailingStop);
        assert!((price - 108.0).abs() < 1e-9, "fill at trail level, got {price}");
    }

    #[test]
    fn max_bars_held_fires_at_close() {
        // LONG entry 100, exit after 2 bars regardless of price.
        // Time exits always fill at close regardless of IntraBarMode.
        let mut tr = PositionTracker::with_levels(100.0, None, None, None, Some(2), true);
        assert!(tr.update_and_check(100.0, 101.0, 99.0, 100.5, IntraBarMode::Pessimistic).is_none());
        let (price, reason) = tr
            .update_and_check(100.5, 102.0, 100.0, 101.0, IntraBarMode::Pessimistic)
            .expect("time exit must fire");
        assert_eq!(reason, ExitReason::MaxBarsHeld);
        assert!((price - 101.0).abs() < 1e-9);
    }
}
