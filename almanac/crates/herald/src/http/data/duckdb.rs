//! DuckDB-powered ad-hoc SQL endpoint (`POST /api/data/duckdb`).
//!
//! The parquet fallback helpers (called from `ohlcv.rs` and `unified.rs`) live
//! in `http::duckdb_helpers` (the old `duckdb.rs` content minus this handler).

use std::sync::Arc;

use axum::{extract::State, http::StatusCode, response::IntoResponse, Json, Router};
use axum::routing::post;
use tracing::{info, warn};

use super::super::duckdb_helpers::{DuckQueryReq, run_query};
use super::super::HttpState;

// ── Routes ────────────────────────────────────────────────────────────────────

pub fn routes() -> Router<HttpState> {
    Router::new().route("/api/data/duckdb", post(query))
}

// ── Handler ───────────────────────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/data/duckdb",
    responses(
        (status = 200, description = "DuckDB query results"),
        (status = 400, description = "Bad request or invalid SQL", body = super::super::types::ErrorResponse)
    ),
    tag = "live"
)]
pub async fn query(State(state): State<HttpState>, Json(req): Json<DuckQueryReq>) -> impl IntoResponse {
    let data_dir = Arc::clone(&state.data_dir);
    tokio::task::spawn_blocking(move || run_query(&data_dir, req))
        .await
        .unwrap_or_else(|e| Err(anyhow::anyhow!("task panicked: {e}")))
        .map(|v| Json(v).into_response())
        .unwrap_or_else(|e| {
            warn!(error = %e, "duckdb query failed");
            (StatusCode::BAD_REQUEST, Json(serde_json::json!({"error": e.to_string()}))).into_response()
        })
}
