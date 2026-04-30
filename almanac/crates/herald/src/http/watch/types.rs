//! Domain types for the watchlist feature.

use serde::{Deserialize, Serialize};

use super::super::store::types::StrategySpec;

/// A registered watch — runs a strategy on live bars and dispatches
/// signals to a webhook or NATS subject instead of executing trades.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WatchEntry {
    pub id: String,
    pub symbols: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub timeframe: Option<String>,
    pub spec: StrategySpec,
    /// HTTP endpoint to POST signal JSON to when strategy fires.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub webhook_url: Option<String>,
    /// NATS subject to publish SignalMsg to (e.g. strategist subscribes).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub nats_subject: Option<String>,
    /// Number of indicator handles pinned in the ledger by this entry.
    /// Informational — decrements to 0 when the entry is deleted.
    pub pinned_indicators: usize,
    pub created_at: i64,
}

#[derive(Debug, Deserialize)]
pub struct CreateWatchReq {
    pub symbols: Vec<String>,
    #[serde(default)]
    pub timeframe: Option<String>,
    pub spec: StrategySpec,
    #[serde(default)]
    pub webhook_url: Option<String>,
    #[serde(default)]
    pub nats_subject: Option<String>,
}
