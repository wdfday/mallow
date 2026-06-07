//! Request / response DTOs for the herald HTTP API.
//!
//! Kept in a separate module so handlers stay focused on behaviour and the
//! wire format is easy to audit in one place.

use alm_core::Bar;
use axum::{
    http::StatusCode,
    response::{IntoResponse, Response},
    Json,
};
use serde::{Deserialize, Serialize};
use utoipa::ToSchema;

// ── Standard response envelopes ───────────────────────────────────────────────

/// Success envelope matching the Go `shared.SuccessResponse[T]` shape:
/// `{ "status": 200, "message": "OK", "data": T }`.
#[derive(Debug, Serialize)]
pub struct ApiResponse<T: Serialize> {
    pub status: u16,
    pub message: &'static str,
    pub data: T,
}

/// Error envelope matching the Go `shared.ErrorResponse` shape:
/// `{ "status": 400, "code": "BAD_REQUEST", "message": "..." }`.
#[derive(Debug, Serialize, ToSchema)]
pub struct ErrorResponse {
    pub status: u16,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub code: Option<&'static str>,
    pub message: String,
}

// ── Response helpers (return axum::Response so handlers can use them in match) ─

pub fn ok<T: Serialize + 'static>(data: T) -> Response {
    (StatusCode::OK, Json(ApiResponse { status: 200, message: "OK", data })).into_response()
}

pub fn created<T: Serialize + 'static>(data: T) -> Response {
    (StatusCode::CREATED, Json(ApiResponse { status: 201, message: "Created", data })).into_response()
}

pub fn no_content() -> Response {
    StatusCode::NO_CONTENT.into_response()
}

pub fn err(sc: StatusCode, message: impl Into<String>) -> Response {
    (sc, Json(ErrorResponse { status: sc.as_u16(), code: None, message: message.into() })).into_response()
}

pub fn err_code(sc: StatusCode, code: &'static str, message: impl Into<String>) -> Response {
    (sc, Json(ErrorResponse { status: sc.as_u16(), code: Some(code), message: message.into() })).into_response()
}

// ── Bar / candle payloads ─────────────────────────────────────────────────────

/// Single OHLCV bar on the wire. Short field names to cut payload size.
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

/// Request body for `POST /api/v1/data/:source/:symbol`.
/// Indicator computation is handled client-side via WASM — only raw bars are returned.
#[derive(Debug, Deserialize, ToSchema)]
pub struct UnifiedDataRequest {
    pub tf: Option<String>,
    pub candles: Option<CandlesQuery>,
}

/// Response for `POST /api/v1/data/:source/:symbol` — raw OHLCV bars only.
#[derive(Debug, Serialize, ToSchema)]
pub struct UnifiedDataResponse {
    pub source: String,
    pub symbol: String,
    pub tf: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub candles: Option<CandlesResult>,
}

