//! Request / response DTOs for the herald HTTP API.
//!
//! Kept in a separate module so handlers stay focused on behaviour and the
//! wire format is easy to audit in one place.

use std::collections::HashMap;

use alm_core::Bar;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use utoipa::ToSchema;

// ── Error envelope ────────────────────────────────────────────────────────────

#[derive(Debug, Serialize, ToSchema)]
pub struct ErrorResponse {
    pub error: String,
}

impl ErrorResponse {
    pub fn new(msg: impl Into<String>) -> Self {
        Self { error: msg.into() }
    }
}

// ── Bar / candle payloads ─────────────────────────────────────────────────────

/// Single OHLCV bar on the wire. Short field names to cut payload size; the
/// client is a chart with millisecond-scale rendering budgets.
#[derive(Debug, Serialize, ToSchema)]
pub struct BarRecord {
    /// Unix milliseconds (UTC).
    pub t: i64,
    pub o: f64,
    pub h: f64,
    pub l: f64,
    pub c: f64,
    pub v: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub vwap: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub n: Option<i64>,
}

impl From<&Bar> for BarRecord {
    fn from(b: &Bar) -> Self {
        Self {
            t: b.timestamp,
            o: b.open,
            h: b.high,
            l: b.low,
            c: b.close,
            v: b.volume,
            vwap: b.vwap,
            n: b.transactions,
        }
    }
}

/// Response envelope for `GET /api/data/:symbol` and `GET /api/data/:symbol/latest`.
///
/// `next_before` / `next_after` carry the cursor the client should pass on the
/// next scroll request — absent when there is nothing left in that direction.
#[derive(Debug, Serialize, ToSchema)]
pub struct DataResponse {
    pub symbol: String,
    pub tf: String,
    pub count: usize,
    pub bars: Vec<BarRecord>,
    /// `t` of the oldest returned bar. Pass as `?before=` to fetch the prior page.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub next_before: Option<i64>,
    /// `t` of the newest returned bar. Pass as `?after=` to fetch the next page.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub next_after: Option<i64>,
    /// True when `next_before` points at bars that are older than anything
    /// currently in the ledger window — the client should route the next
    /// request to cold / walk-back storage (future work).
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub truncated_below: bool,
}

// ── Data query parameters ─────────────────────────────────────────────────────

/// Query string for `GET /api/data/:symbol`.
#[derive(Debug, Deserialize, ToSchema, utoipa::IntoParams)]
pub struct DataQuery {
    /// Bars with `t < before`, newest-first. Unix milliseconds.
    pub before: Option<i64>,
    /// Bars with `t > after`, oldest-first. Unix milliseconds.
    pub after: Option<i64>,
    /// Max bars to return. Default 500, max 5000.
    pub limit: Option<usize>,
    /// Override timeframe — e.g. `tf=M1` when the herald instance also runs M5.
    pub tf: Option<String>,
}

/// Query string for `GET /api/data/:symbol/latest`.
#[derive(Debug, Deserialize, ToSchema, utoipa::IntoParams)]
pub struct LatestQuery {
    /// Number of bars to return. Default 500, max 5000.
    pub n: Option<usize>,
    pub tf: Option<String>,
}

// ── Unified data (POST /api/data/:symbol) ────────────────────────────────────

/// Candle section controls of a unified request.
#[derive(Debug, Deserialize, ToSchema)]
pub struct CandlesQuery {
    /// Max bars to return. Default 500, max 5000.
    pub limit: Option<usize>,
    /// Bars with `t < before`, newest-first. Unix milliseconds.
    pub before: Option<i64>,
    /// Bars with `t > after`, oldest-first. Unix milliseconds.
    pub after: Option<i64>,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct CandlesResult {
    pub count: usize,
    pub bars: Vec<BarRecord>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub next_before: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub next_after: Option<i64>,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub truncated_below: bool,
}

/// Single indicator spec in a unified request. Flat form — anything the
/// `alm_indicator::IndicatorBox::from_config` accepts, plus an optional
/// `label` to override the response key.
///
/// Example: `{"type":"ema","period":20}` or `{"type":"rsi","period":14,"label":"rsi14"}`.
#[derive(Debug, Clone, Deserialize, ToSchema)]
pub struct IndicatorConfig {
    /// Override the key under which the series appears in the response.
    pub label: Option<String>,
    /// Passed verbatim to `IndicatorBox::from_config`. `"type"` is required.
    #[serde(flatten)]
    #[schema(value_type = Object)]
    pub config: serde_json::Map<String, Value>,
}

#[derive(Debug, Deserialize, ToSchema)]
pub struct UnifiedDataRequest {
    pub tf: Option<String>,
    pub candles: Option<CandlesQuery>,
    pub indicators: Option<Vec<IndicatorConfig>>,
}

/// Individual indicator point in a series — `t` plus the indicator's fields.
/// Kept flat so clients can address `row.value` / `row.macd` etc.
#[derive(Debug, Serialize, ToSchema)]
pub struct IndicatorPoint {
    pub t: i64,
    #[serde(flatten)]
    #[schema(value_type = Object)]
    pub fields: HashMap<String, f64>,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct UnifiedDataResponse {
    pub symbol: String,
    pub tf: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub candles: Option<CandlesResult>,
    /// Keyed by `label` or the indicator's canonical key when `label` is omitted.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub indicators: Option<HashMap<String, Vec<IndicatorPoint>>>,
    /// Specs that were requested but could not be satisfied — bad config,
    /// missing `"type"`, exceeded `max_period`, etc. Empty on the happy path.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub missing: Vec<String>,
}
