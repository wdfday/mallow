//! Domain types for the strategy store.

use serde::{Deserialize, Serialize};
use serde_json::Value;
use utoipa::ToSchema;

// ── StrategySpec ──────────────────────────────────────────────────────────────

/// A script strategy specification.
///
/// JSON shape: `{ "script": "let rsi = ind.rsi(14); ..." }` — no `kind` discriminant needed.
/// Legacy records that include `"kind": "script"` are deserialized correctly (unknown fields
/// are ignored by serde).
#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct StrategySpec {
    pub script: String,
}

impl StrategySpec {
    pub fn to_factory_args(&self) -> (String, Value) {
        ("script".into(), serde_json::json!({ "script": self.script }))
    }

    /// Always `"script"` — kept for logging / DB writes.
    pub fn kind_str(&self) -> &'static str { "script" }
}

// ── Stored entities ───────────────────────────────────────────────────────────

/// A versioned strategy definition. Each (name, version) pair is immutable
/// once created — create a new version to iterate on a spec.
#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct Strategy {
    /// UUID v7 — identifies this exact version.
    pub id: String,
    /// Stable slug that groups all versions of the same strategy.
    pub name: String,
    /// User-assigned version number (1, 2, 3 …). Auto-incremented if omitted.
    pub version: i32,
    /// UUID of the strategy version this was branched/updated from.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub previous_id: Option<String>,
    /// Human-readable display label.
    pub label: String,
    #[serde(rename = "strategy_spec")]
    pub spec: StrategySpec,
    /// Optional change notes for this version.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub notes: Option<String>,
    /// Owner user ID (populated from X-User-ID header; null for system/backtest-created).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub user_id: Option<String>,
    pub created_at: i64,
}

// ── Request DTOs ──────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, ToSchema)]
pub struct CreateStrategyReq {
    pub name: String,
    /// If omitted, auto-incremented from the highest existing version for `name`.
    pub version: Option<i32>,
    /// UUID of the strategy version this is branched/updated from.
    pub previous_id: Option<String>,
    pub label: String,
    #[serde(rename = "strategy_spec")]
    pub spec: StrategySpec,
    pub notes: Option<String>,
    /// Override owner user ID. If absent, extracted from X-User-ID header.
    pub user_id: Option<String>,
}

/// Only label and notes are mutable on an existing version.
#[derive(Debug, Deserialize, ToSchema)]
pub struct UpdateStrategyReq {
    pub label: Option<String>,
    pub notes: Option<String>,
}
