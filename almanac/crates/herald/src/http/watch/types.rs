//! Domain types for the watch module.

use std::collections::HashMap;
use std::sync::Arc;

use alm_ledger::IndicatorHandle;
use serde::{Deserialize, Serialize};
use tokio::sync::RwLock;
use utoipa::ToSchema;

use crate::http::strategy::types::StrategySpec;

// ── Domain types ──────────────────────────────────────────────────────────────

/// A registered watch — runs a strategy on live bars and dispatches
/// signals to a webhook or NATS subject instead of executing trades.
#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct WatchEntry {
    pub id: String,
    pub symbols: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub timeframe: Option<String>,
    #[serde(rename = "strategy_spec")]
    pub spec: StrategySpec,
    /// HTTP endpoint to POST signal JSON to when strategy fires.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub webhook_url: Option<String>,
    /// NATS subject to publish SignalMsg to.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub nats_subject: Option<String>,
    /// Owner user ID (populated from X-User-ID header).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub user_id: Option<String>,
    /// Number of indicator handles pinned in the ledger by this entry.
    pub pinned_indicators: usize,
    pub created_at: i64,
}

#[derive(Debug, Deserialize, ToSchema)]
pub struct CreateWatchReq {
    pub symbols: Vec<String>,
    #[serde(default)]
    pub timeframe: Option<String>,
    #[serde(rename = "strategy_spec")]
    pub spec: StrategySpec,
    #[serde(default)]
    pub webhook_url: Option<String>,
    #[serde(default)]
    pub nats_subject: Option<String>,
    /// Override owner user ID. If absent, extracted from X-User-ID header.
    pub user_id: Option<String>,
}

/// Partial update for a watch entry.
///
/// Only provided fields are applied. `symbols` and `strategy_spec` trigger a
/// full handle re-pin (old handles dropped, new ones acquired).
#[derive(Debug, Deserialize, ToSchema)]
pub struct UpdateWatchReq {
    /// Replace the symbol list. Triggers indicator handle re-pin.
    #[serde(default)]
    pub symbols: Option<Vec<String>>,
    /// Replace the strategy spec. Triggers indicator handle re-pin.
    #[serde(default, rename = "strategy_spec")]
    pub spec: Option<StrategySpec>,
    /// Replace the timeframe. Triggers indicator handle re-pin.
    #[serde(default)]
    pub timeframe: Option<String>,
    /// Update the webhook URL. Pass `null` to clear.
    #[serde(default)]
    pub webhook_url: Option<String>,
    /// Update the NATS subject. Pass `null` to clear.
    #[serde(default)]
    pub nats_subject: Option<String>,
}

// ── WatchSlot ─────────────────────────────────────────────────────────────────

/// Internal storage unit. `entry` is what we expose over HTTP; `_handles`
/// keeps the indicator refcounts alive for the lifetime of the slot.
/// Dropping a `WatchSlot` releases all handles (refcount--).
pub struct WatchSlot {
    pub entry: WatchEntry,
    pub _handles: Vec<IndicatorHandle>,
}

// ── Store ─────────────────────────────────────────────────────────────────────

pub type WatchStore = Arc<RwLock<HashMap<String, WatchSlot>>>;

pub fn new_store() -> WatchStore {
    Arc::new(RwLock::new(HashMap::new()))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

pub fn parse_tf(s: &str) -> Option<alm_core::Timeframe> {
    match s.to_ascii_uppercase().as_str() {
        "M1"  => Some(alm_core::Timeframe::M1),
        "M5"  => Some(alm_core::Timeframe::M5),
        "M15" => Some(alm_core::Timeframe::M15),
        "M30" => Some(alm_core::Timeframe::M30),
        "H1"  => Some(alm_core::Timeframe::H1),
        "H4"  => Some(alm_core::Timeframe::H4),
        "D1"  => Some(alm_core::Timeframe::D1),
        "W1"  => Some(alm_core::Timeframe::W1),
        _     => None,
    }
}
