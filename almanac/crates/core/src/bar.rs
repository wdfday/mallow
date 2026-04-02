use serde::{Deserialize, Serialize};

/// OHLCV bar — unit of data for backtesting.
/// Timestamps in Unix milliseconds, consistent with us-data and stream-data.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Bar {
    /// Unix timestamp in milliseconds (bar open time)
    pub timestamp: i64,
    pub symbol: String,
    pub open: f64,
    pub high: f64,
    pub low: f64,
    pub close: f64,
    pub volume: f64,
    pub vwap: Option<f64>,
    pub transactions: Option<i64>,
}

impl Bar {
    pub fn new(
        timestamp: i64,
        symbol: impl Into<String>,
        open: f64,
        high: f64,
        low: f64,
        close: f64,
        volume: f64,
    ) -> Self {
        Self {
            timestamp,
            symbol: symbol.into(),
            open,
            high,
            low,
            close,
            volume,
            vwap: None,
            transactions: None,
        }
    }

    pub fn typical_price(&self) -> f64 {
        (self.high + self.low + self.close) / 3.0
    }
}

/// Real-time tick — individual trade event from stream-data.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Tick {
    pub timestamp: i64,
    pub symbol: String,
    pub price: f64,
    pub volume: f64,
    pub bid: Option<f64>,
    pub ask: Option<f64>,
}
