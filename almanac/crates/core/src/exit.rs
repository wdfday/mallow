/// Exit rules applied on top of a strategy's own signal logic.
///
/// The engine evaluates these rules every bar while a position is open.
/// Rules are checked in order: stop-loss → take-profit → trailing-stop → max-bars.
/// The first rule that fires emits a `Signal::close`.
///
/// All rules are optional; unset rules are skipped.
#[derive(Debug, Clone, Default)]
pub struct ExitRules {
    /// Cut-loss: close if price falls this fraction below entry.
    /// E.g. `0.05` = cut at 5 % loss.
    pub stop_loss_pct: Option<f64>,

    /// Take-profit: close if price rises this fraction above entry.
    /// E.g. `0.10` = take profit at 10 % gain.
    pub take_profit_pct: Option<f64>,

    /// Trailing stop: close if price retreats this fraction from the
    /// highest price seen since entry.
    /// E.g. `0.02` = trail at 2 %.
    pub trailing_stop_pct: Option<f64>,

    /// Time-based exit: force-close after this many bars in position.
    pub max_bars_held: Option<usize>,
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
    /// Highest close seen since entry (used for trailing stop).
    pub highest_price: f64,
    /// Number of bars the position has been open.
    pub bars_held: usize,
}

impl PositionTracker {
    pub fn new(entry_price: f64) -> Self {
        Self {
            entry_price,
            highest_price: entry_price,
            bars_held: 0,
        }
    }

    /// Update tracker with the current bar's close.  Returns `true` if any
    /// exit rule in `rules` is triggered.
    pub fn update_and_check(&mut self, price: f64, rules: &ExitRules) -> bool {
        self.highest_price = self.highest_price.max(price);
        self.bars_held += 1;

        if let Some(sl) = rules.stop_loss_pct {
            if price <= self.entry_price * (1.0 - sl) {
                return true;
            }
        }
        if let Some(tp) = rules.take_profit_pct {
            if price >= self.entry_price * (1.0 + tp) {
                return true;
            }
        }
        if let Some(ts) = rules.trailing_stop_pct {
            if price <= self.highest_price * (1.0 - ts) {
                return true;
            }
        }
        if let Some(max) = rules.max_bars_held {
            if self.bars_held >= max {
                return true;
            }
        }
        false
    }
}
