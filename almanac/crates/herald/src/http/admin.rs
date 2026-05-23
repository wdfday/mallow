//! Admin-only HTTP endpoints.
//!
//! All routes are under `/api/v1/admin/`. The api-gateway enforces auth before
//! these handlers are reached, so no per-request auth logic is needed here.

use axum::{extract::State, routing::get, Json, Router};

use super::HttpState;
use crate::ws_latency::SourceStats;

// ── Router ────────────────────────────────────────────────────────────────────

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/v1/admin/ws-latency", get(ws_latency_handler))
}

// ── Handler ───────────────────────────────────────────────────────────────────

/// `GET /api/v1/admin/ws-latency`
///
/// Returns WebSocket bar delivery latency statistics per exchange source.
///
/// Latency is defined as:
/// ```text
/// latency_ms = received_at_ms − (bar.open_time_ms + tf.duration_ms())
/// ```
/// i.e. milliseconds elapsed between the bar's theoretical close time and when
/// herald received the WebSocket message. A value of ~50 ms is typical; higher
/// values indicate network or exchange-side delays.
///
/// Statistics are computed over a sliding window of the last 1 000 closed bars
/// per source.
async fn ws_latency_handler(
    State(state): State<HttpState>,
) -> Json<std::collections::HashMap<String, SourceStats>> {
    Json(state.ws_latency.snapshot())
}
