use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum Side {
    Buy,
    Sell,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub enum OrderKind {
    Market,
    Limit { price: f64 },
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct OrderRequest {
    pub timestamp: i64,
    pub symbol: String,
    pub side: Side,
    pub kind: OrderKind,
    pub qty: f64,
}

impl OrderRequest {
    pub fn market(timestamp: i64, symbol: impl Into<String>, side: Side, qty: f64) -> Self {
        Self { timestamp, symbol: symbol.into(), side, kind: OrderKind::Market, qty }
    }
}

/// Confirmed execution of an order.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Fill {
    pub timestamp: i64,
    pub symbol: String,
    pub side: Side,
    pub qty: f64,
    pub price: f64,
    pub commission: f64,
}

impl Fill {
    /// Net cash impact: negative for buys, positive for sells (after commission).
    pub fn cash_impact(&self) -> f64 {
        let gross = self.qty * self.price;
        match self.side {
            Side::Buy => -(gross + self.commission),
            Side::Sell => gross - self.commission,
        }
    }
}
