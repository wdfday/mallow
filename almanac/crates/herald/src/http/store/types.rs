//! Domain types for the strategy store + backtest case/result persistence.

use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;
use utoipa::ToSchema;

use alm_engine::types::ExitConfig;

// ── StrategySpec ──────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum StrategySpec {
    Cel {
        entry: String,
        exit: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        defines: Option<HashMap<String, String>>,
    },
    Rhai {
        script: String,
    },
}

impl StrategySpec {
    pub fn to_factory_args(&self) -> (String, Value) {
        match self {
            StrategySpec::Cel { entry, exit, defines } => {
                let mut m = serde_json::Map::new();
                m.insert("entry".into(), Value::String(entry.clone()));
                m.insert("exit".into(), Value::String(exit.clone()));
                if let Some(defs) = defines {
                    if let Ok(v) = serde_json::to_value(defs) {
                        m.insert("defines".into(), v);
                    }
                }
                ("cel".into(), Value::Object(m))
            }
            StrategySpec::Rhai { script } => {
                ("rhai".into(), serde_json::json!({ "script": script }))
            }
        }
    }

    pub fn kind_str(&self) -> &'static str {
        match self {
            StrategySpec::Cel { .. }  => "cel",
            StrategySpec::Rhai { .. } => "rhai",
        }
    }
}

// ── Capital / execution sub-configs ──────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct CapitalConfig {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub initial: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub position_pct: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub position_usd: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_positions: Option<usize>,
}

impl Default for CapitalConfig {
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
}

impl Default for ExecutionConfig {
    fn default() -> Self {
        Self {
            commission_pct:   Some(0.001),
            slippage_pct:     Some(0.0005),
            risk_free_annual: Some(0.04),
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
    /// Human-readable display label.
    pub label: String,
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
    pub capital: CapitalConfig,
    pub execution: ExecutionConfig,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub exit: Option<ExitConfig>,
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
    /// `"long"`, `"short"`, or `"close"`.
    pub direction: String,
    /// Bar close price at signal time.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub price: Option<f64>,
    /// Signal strength \[0, 1\].
    pub strength: f64,
    /// Take-profit price (from Rhai `let tp = …`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub target_price: Option<f64>,
    /// Stop-loss price (from Rhai `let sl = …`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stop_price: Option<f64>,
}

// ── Request DTOs ──────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, ToSchema)]
pub struct CreateStrategyReq {
    pub name: String,
    /// If omitted, auto-incremented from the highest existing version for `name`.
    pub version: Option<i32>,
    pub label: String,
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
    pub capital: Option<CapitalConfig>,
    #[serde(default)]
    pub execution: Option<ExecutionConfig>,
    #[serde(default)]
    pub exit: Option<ExitConfig>,
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
    pub capital: Option<CapitalConfig>,
    pub execution: Option<ExecutionConfig>,
    pub exit: Option<ExitConfig>,
}
