use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum Direction {
    Long,
    Short,
    /// Close existing position (no new side)
    Close,
}

/// Optional pattern metadata attached to a signal.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct PatternMeta {
    pub pattern_kind: String,      // e.g. "bull_flag", "ascending_triangle"
    pub confidence: f64,           // [0.0, 1.0]
    pub target_price: Option<f64>, // projected price target
    pub stop_price: Option<f64>,   // invalidation stop level
}

/// Trading signal emitted by a strategy.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Signal {
    pub timestamp: i64,
    pub symbol: String,
    pub direction: Direction,
    /// Conviction strength [0.0, 1.0] — used by RiskManager for position sizing
    pub strength: f64,
    /// Optional pattern metadata — populated by pattern_breakout strategy
    pub pattern: Option<PatternMeta>,
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
}

impl Signal {
    pub fn long(timestamp: i64, symbol: impl Into<String>, strength: f64) -> Self {
        Self { timestamp, symbol: symbol.into(), direction: Direction::Long, strength, pattern: None, price: None, target_price: None, stop_price: None, is_offset: false, reason: None }
    }

    pub fn short(timestamp: i64, symbol: impl Into<String>, strength: f64) -> Self {
        Self { timestamp, symbol: symbol.into(), direction: Direction::Short, strength, pattern: None, price: None, target_price: None, stop_price: None, is_offset: false, reason: None }
    }

    pub fn close(timestamp: i64, symbol: impl Into<String>) -> Self {
        Self { timestamp, symbol: symbol.into(), direction: Direction::Close, strength: 1.0, pattern: None, price: None, target_price: None, stop_price: None, is_offset: false, reason: None }
    }

    pub fn with_pattern(mut self, meta: PatternMeta) -> Self {
        self.pattern = Some(meta);
        self
    }
}
