use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum Direction {
    Long,
    Short,
    /// Exit existing position (no new side)
    Exit,
}

/// Trading signal emitted by a strategy.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Signal {
    pub timestamp: i64,
    pub symbol: String,
    pub direction: Direction,
    /// Conviction strength [0.0, 1.0] — used by RiskManager for position sizing
    pub strength: f64,
    /// bar.close at the time this signal fired — entry reference price for the receiver.
    pub price: Option<f64>,
    /// Take-profit level. Absolute price if is_offset=false, delta from price if is_offset=true.
    pub target_price: Option<f64>,
    /// Stop-loss level. Absolute price if is_offset=false, delta from price if is_offset=true.
    pub stop_price: Option<f64>,
    /// When true, target_price and stop_price are offsets from the price field (not absolute levels).
    /// Helm computes actual levels as fill_price + target_price / fill_price + stop_price.
    pub is_offset: bool,
    /// Human-readable reason for this signal — logged by helm for auditability.
    /// E.g. `"ema cross above H4, rsi < 60"`. Not used for execution logic.
    pub reason: Option<String>,
    /// ATR (Average True Range) at bar close — forwarded to helm for stop/TP sizing.
    /// Set by the strategy or computed from the ledger ATR(14) at signal time.
    pub atr: Option<f64>,
    /// Trailing-stop fraction from the running peak (long) / trough (short).
    /// E.g. `0.05` = exit when price retraces 5% from the best price seen.
    pub trailing_stop_pct: Option<f64>,
    /// Time-based exit: force-close after this many bars in position.
    pub max_bars_held: Option<usize>,
}

impl Signal {
    pub fn long(timestamp: i64, symbol: impl Into<String>, strength: f64) -> Self {
        Self { timestamp, symbol: symbol.into(), direction: Direction::Long, strength, price: None, target_price: None, stop_price: None, is_offset: false, reason: None, atr: None, trailing_stop_pct: None, max_bars_held: None }
    }

    pub fn short(timestamp: i64, symbol: impl Into<String>, strength: f64) -> Self {
        Self { timestamp, symbol: symbol.into(), direction: Direction::Short, strength, price: None, target_price: None, stop_price: None, is_offset: false, reason: None, atr: None, trailing_stop_pct: None, max_bars_held: None }
    }

    pub fn exit(timestamp: i64, symbol: impl Into<String>) -> Self {
        Self { timestamp, symbol: symbol.into(), direction: Direction::Exit, strength: 1.0, price: None, target_price: None, stop_price: None, is_offset: false, reason: None, atr: None, trailing_stop_pct: None, max_bars_held: None }
    }

    pub fn with_atr(mut self, atr: f64) -> Self {
        self.atr = Some(atr);
        self
    }
}
