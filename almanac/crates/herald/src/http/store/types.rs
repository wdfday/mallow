//! Domain types for the strategy store + backtest case/result persistence.

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

// ── Capital / execution sub-configs ──────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct PositionConfig {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub initial: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub position_pct: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub position_usd: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_positions: Option<usize>,
}

impl Default for PositionConfig {
    fn default() -> Self {
        Self {
            initial:       Some(10_000.0),
            position_pct:  Some(0.95),
            position_usd:  None,
            max_positions: Some(1),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct ExecutionConfig {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub commission_pct: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub slippage_pct: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub risk_free_annual: Option<f64>,
    /// `"close_only"` (default) | `"pessimistic"` | `"ohlc_heuristic"`
    #[serde(skip_serializing_if = "Option::is_none")]
    pub intra_bar_mode: Option<String>,
}

impl Default for ExecutionConfig {
    fn default() -> Self {
        Self {
            commission_pct:   Some(0.001),
            slippage_pct:     Some(0.0005),
            risk_free_annual: Some(0.04),
            intra_bar_mode:   None,
        }
    }
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
    pub created_at: i64,
}

/// A parameterised backtest run configuration linked to a specific strategy version.
#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct BacktestCase {
    pub id: String,
    /// Points to a specific Strategy version UUID.
    pub strategy_id: String,
    pub label: String,
    pub symbol: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub timeframe: Option<String>,
    /// Inclusive start of the backtest window (Unix ms). `None` = feed start.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub from_ms: Option<i64>,
    /// Inclusive end of the backtest window (Unix ms). `None` = feed end.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub to_ms: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data_source: Option<String>,
    pub capital: PositionConfig,
    pub execution: ExecutionConfig,
    pub created_at: i64,
    pub updated_at: i64,
}

/// Summary row written after each successful run. Full report pushed to S3.
#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct BacktestResult {
    pub id: String,
    pub case_id: String,
    pub ran_at: i64,
    /// S3 / R2 object key for the full BacktestReport JSON.
    /// `None` when upload is disabled or failed.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub s3_key: Option<String>,
    pub total_return_pct: f64,
    pub sharpe_ratio: f64,
    pub max_drawdown_pct: f64,
    pub win_rate_pct: f64,
    pub total_trades: i64,
    pub created_at: i64,
}

/// One signal emitted during a signal-replay run.
#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct SignalPoint {
    /// Unix milliseconds of the bar that triggered this signal.
    pub ts: i64,
    /// `"long"`, `"short"`, or `"exit"`.
    pub direction: String,
    /// Bar close price at signal time.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub price: Option<f64>,
    /// Signal strength \[0, 1\].
    pub strength: f64,
    /// Take-profit price (from script `let tp = …`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub target_price: Option<f64>,
    /// Stop-loss price (from script `let sl = …`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stop_price: Option<f64>,
    /// True when `tp`/`sl` are offsets from fill price, not absolute prices.
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub is_offset: bool,
    /// Human-readable reason string for audit logs.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reason: Option<String>,
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
}

/// Only label and notes are mutable on an existing version.
#[derive(Debug, Deserialize, ToSchema)]
pub struct UpdateStrategyReq {
    pub label: Option<String>,
    pub notes: Option<String>,
}

#[derive(Debug, Deserialize, ToSchema)]
pub struct CreateCaseReq {
    pub label: String,
    pub strategy_id: String,
    pub symbol: String,
    #[serde(default)]
    pub timeframe: Option<String>,
    #[serde(default)]
    pub from_ms: Option<i64>,
    #[serde(default)]
    pub to_ms: Option<i64>,
    #[serde(default)]
    pub data_source: Option<String>,
    #[serde(default)]
    pub capital: Option<PositionConfig>,
    #[serde(default)]
    pub execution: Option<ExecutionConfig>,
}

#[derive(Debug, Deserialize, ToSchema)]
pub struct UpdateCaseReq {
    pub label: Option<String>,
    pub strategy_id: Option<String>,
    pub symbol: Option<String>,
    pub timeframe: Option<String>,
    pub from_ms: Option<i64>,
    pub to_ms: Option<i64>,
    pub data_source: Option<String>,
    pub capital: Option<PositionConfig>,
    pub execution: Option<ExecutionConfig>,
}
