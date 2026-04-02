use crate::{Bar, Fill, OrderRequest, Signal};
use serde::{Deserialize, Serialize};

/// Core event types for the event-driven backtesting engine.
/// Every component communicates through events — no direct coupling.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub enum Event {
    Market(MarketEvent),
    Signal(SignalEvent),
    Order(OrderEvent),
    Fill(FillEvent),
    Equity(EquityEvent),
}

/// New bar arrived from the data feed.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MarketEvent {
    pub bar: Bar,
}

/// Strategy emits a trading signal.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SignalEvent {
    pub signal: Signal,
}

/// Risk-validated order ready for execution.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OrderEvent {
    pub order: OrderRequest,
}

/// Broker executed an order.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FillEvent {
    pub fill: Fill,
}

/// Portfolio equity snapshot.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EquityEvent {
    pub timestamp: i64,
    pub equity: f64,
    pub cash: f64,
}
