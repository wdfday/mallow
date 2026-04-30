use crate::{exit::ExitReason, order::Side};
use serde::{Deserialize, Serialize};

/// A completed round-trip trade (entry + exit).
/// Used for win rate, profit factor, and trade statistics.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Trade {
    pub symbol: String,
    pub side: Side,
    pub qty: f64,
    pub entry_price: f64,
    pub exit_price: f64,
    pub entry_timestamp: i64,
    pub exit_timestamp: i64,
    /// Realized PnL after commissions
    pub pnl: f64,
    pub pnl_pct: f64,
    /// Total commission paid (entry + exit) for this trade.
    pub commission: f64,
    /// Maximum Adverse Excursion — worst unrealized loss from entry, as a fraction.
    pub mae_pct: f64,
    /// Maximum Favorable Excursion — best unrealized gain from entry, as a fraction.
    pub mfe_pct: f64,
    /// Number of bars this position was held.
    pub bars_held: usize,
    /// Why this trade was closed.
    pub exit_reason: ExitReason,
}

impl Trade {
    pub fn is_winner(&self) -> bool {
        self.pnl > 0.0
    }

    /// Duration in milliseconds
    pub fn duration_ms(&self) -> i64 {
        self.exit_timestamp - self.entry_timestamp
    }

    pub fn duration_hours(&self) -> f64 {
        self.duration_ms() as f64 / 3_600_000.0
    }
}
