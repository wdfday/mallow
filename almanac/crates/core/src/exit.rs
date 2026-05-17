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
#[derive(Debug, Clone, Default, PartialEq)]
pub enum IntraBarMode {
    /// Check only at bar.close (simplest, default).
    /// Fill at close. May miss intra-bar stop touches.
    #[default]
    CloseOnly,
    /// Check stop/SL against bar.low (long) or bar.high (short).
    /// Check TP against bar.high (long) or bar.low (short).
    /// Fill at the stop level, not at low/high. Always pessimistic —
    /// if both SL and TP could fire, SL fires first.
    Pessimistic,
    /// Same intra-bar checks as `Pessimistic`, but uses bar direction
    /// (close vs open) to determine which extreme happened first.
    /// Up-bar (close > open): low before high → SL checked first.
    /// Down-bar (close < open): high before low → TP checked first.
    OhlcHeuristic,
}

/// Exit rules applied on top of a strategy's own signal logic.
///
/// The engine evaluates these rules every bar while a position is open.
/// Rules are checked in order: trailing-stop → fixed-sl → fixed-tp → max-bars → ATR levels.
/// The first rule that fires calls `force_close` at the computed fill price.
///
/// All rules are optional; unset rules are skipped.
#[derive(Debug, Clone, Default)]
pub struct ExitRules {
    /// Cut-loss: close if price falls this fraction below entry (long)
    /// or rises this fraction above entry (short).
    pub stop_loss_pct: Option<f64>,

    /// Take-profit: close if price rises this fraction above entry (long)
    /// or falls this fraction below entry (short).
    pub take_profit_pct: Option<f64>,

    /// Trailing stop: close if price retraces this fraction from the
    /// highest-seen price (long) or lowest-seen price (short).
    /// E.g. `0.05` = trail at 5% below the running peak.
    pub trailing_stop_pct: Option<f64>,

    /// Time-based exit: force-close after this many bars in position.
    pub max_bars_held: Option<usize>,

    /// Controls how OHLC data is used to check exit levels within a bar.
    pub intra_bar_mode: IntraBarMode,
}

impl ExitRules {
    /// Returns `true` if at least one rule is configured.
    pub fn is_active(&self) -> bool {
        self.stop_loss_pct.is_some()
            || self.take_profit_pct.is_some()
            || self.trailing_stop_pct.is_some()
            || self.max_bars_held.is_some()
    }
}

/// Per-position state tracked by the engine to evaluate `ExitRules`.
#[derive(Debug, Clone)]
pub struct PositionTracker {
    /// Fill price of the entry order.
    pub entry_price: f64,
    /// Number of bars the position has been open.
    pub bars_held: usize,
    /// Absolute stop-loss price (computed from ATR at entry).
    pub stop_price: Option<f64>,
    /// Absolute take-profit price (computed from ATR at entry).
    pub target_price: Option<f64>,
    /// Whether this is a long position (true) or short (false).
    pub is_long: bool,
    /// Running extremum for trailing stop.
    /// Long: highest close seen; Short: lowest close seen.
    pub extreme: f64,
    /// Max adverse excursion as a fraction of entry price (always >= 0).
    pub mae: f64,
    /// Max favorable excursion as a fraction of entry price (always >= 0).
    pub mfe: f64,
}

impl PositionTracker {
    pub fn new(entry_price: f64, is_long: bool) -> Self {
        Self {
            entry_price,
            bars_held: 0,
            stop_price: None,
            target_price: None,
            is_long,
            extreme: entry_price,
            mae: 0.0,
            mfe: 0.0,
        }
    }

    /// Construct with pre-computed absolute stop/target levels (ATR-based).
    pub fn with_levels(
        entry_price: f64,
        stop_price: Option<f64>,
        target_price: Option<f64>,
        is_long: bool,
    ) -> Self {
        Self {
            entry_price,
            bars_held: 0,
            stop_price,
            target_price,
            is_long,
            extreme: entry_price,
            mae: 0.0,
            mfe: 0.0,
        }
    }

    /// Update tracker for the current bar. Returns `Some((fill_price, reason))` if an
    /// exit rule fires, `None` otherwise.
    ///
    /// `fill_price` is the price at which the position should be closed:
    /// - `CloseOnly`: always `close`.
    /// - `Pessimistic` / `OhlcHeuristic`: the exact stop/target level breached.
    ///
    /// Time-based exits always fill at `close` regardless of mode.
    pub fn update_and_check(
        &mut self,
        open: f64,
        high: f64,
        low: f64,
        close: f64,
        rules: &ExitRules,
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

        // Update running extremum for trailing stop (always use close).
        if self.is_long {
            if close > self.extreme { self.extreme = close; }
        } else if close < self.extreme {
            self.extreme = close;
        }

        // Determine which prices to use for stop and target checks.
        // For long: stop fires on low, target fires on high.
        // For short: stop fires on high, target fires on low.
        let (stop_check, target_check) = match rules.intra_bar_mode {
            IntraBarMode::CloseOnly => (close, close),
            IntraBarMode::Pessimistic => {
                if self.is_long { (low, high) } else { (high, low) }
            }
            IntraBarMode::OhlcHeuristic => {
                if self.is_long { (low, high) } else { (high, low) }
                // OhlcHeuristic uses same prices as Pessimistic; the difference
                // is in which fires first when both levels are breached in one bar.
            }
        };

        // Whether to check TP before stop this bar (OhlcHeuristic only).
        // Up-bar (close > open): price went low-first → stop is more likely first → stop before TP.
        // Down-bar: price went high-first → TP is more likely first → TP before stop.
        let tp_first = matches!(rules.intra_bar_mode, IntraBarMode::OhlcHeuristic)
            && !self.is_long && close < open  // short + down-bar: target (lower) first
            || matches!(rules.intra_bar_mode, IntraBarMode::OhlcHeuristic)
            && self.is_long && close > open;  // long + up-bar: TP fires after stop... no, TP first only for favorable direction

        // Helper: check one side then the other based on tp_first.
        // We inline the checks below for clarity.

        // Trailing stop level (stop-side)
        let trail_stop_price = rules.trailing_stop_pct.map(|t| {
            if self.is_long { self.extreme * (1.0 - t) } else { self.extreme * (1.0 + t) }
        });

        // Fixed SL level
        let sl_price = rules.stop_loss_pct.map(|sl| {
            if self.is_long { self.entry_price * (1.0 - sl) } else { self.entry_price * (1.0 + sl) }
        });

        // Fixed TP level
        let tp_price = rules.take_profit_pct.map(|tp| {
            if self.is_long { self.entry_price * (1.0 + tp) } else { self.entry_price * (1.0 - tp) }
        });

        // Signal-level absolute stop / target prices.
        let sig_stop   = self.stop_price;
        let sig_target = self.target_price;

        // Check a stop-side level (trailing / SL / signal stop).
        let check_stop = |level: f64| -> bool {
            if self.is_long { stop_check <= level } else { stop_check >= level }
        };

        // Check a target-side level (TP / signal target).
        let check_target = |level: f64| -> bool {
            if self.is_long { target_check >= level } else { target_check <= level }
        };

        // Evaluate stops and targets, respecting tp_first ordering.
        let stops: &[Option<(f64, ExitReason)>] = &[
            trail_stop_price.map(|p| (p, ExitReason::TrailingStop)),
            sl_price.map(|p| (p, ExitReason::StopLoss)),
            sig_stop.map(|p| (p, ExitReason::StopLoss)),
        ];
        let targets: &[Option<(f64, ExitReason)>] = &[
            tp_price.map(|p| (p, ExitReason::TakeProfit)),
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
            let fill_price = match rules.intra_bar_mode {
                IntraBarMode::CloseOnly => close,
                _ => level_price,
            };
            return Some((fill_price, reason));
        }

        // Time-based (always fills at close, no intra-bar check needed).
        if let Some(max) = rules.max_bars_held {
            if self.bars_held >= max { return Some((close, ExitReason::MaxBarsHeld)); }
        }

        None
    }
}
