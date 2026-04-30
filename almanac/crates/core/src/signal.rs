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
    /// Absolute take-profit price computed at entry time.
    pub target_price: Option<f64>,
    /// Absolute stop-loss price computed at entry time.
    pub stop_price: Option<f64>,
}

impl Signal {
    pub fn long(timestamp: i64, symbol: impl Into<String>, strength: f64) -> Self {
        Self { timestamp, symbol: symbol.into(), direction: Direction::Long, strength, pattern: None, price: None, target_price: None, stop_price: None }
    }

    pub fn short(timestamp: i64, symbol: impl Into<String>, strength: f64) -> Self {
        Self { timestamp, symbol: symbol.into(), direction: Direction::Short, strength, pattern: None, price: None, target_price: None, stop_price: None }
    }

    pub fn close(timestamp: i64, symbol: impl Into<String>) -> Self {
        Self { timestamp, symbol: symbol.into(), direction: Direction::Close, strength: 1.0, pattern: None, price: None, target_price: None, stop_price: None }
    }

    pub fn with_pattern(mut self, meta: PatternMeta) -> Self {
        self.pattern = Some(meta);
        self
    }
}
