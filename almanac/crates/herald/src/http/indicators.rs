//! `GET /api/indicators` — static indicator catalogue.
//!
//! Lists every indicator type the system can build, with parameter schemas
//! and CEL aliases. Backed by [`alm_strategy::catalog`] so the live and
//! backtest surfaces share one source of truth.
//!
//! Live per-symbol indicator state is served by `GET /api/symbols?indicators=true`.

use axum::{routing::get, Json, Router};
use alm_strategy::catalog::{self, IndicatorMeta};

use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new().route("/api/indicators", get(list_indicators_catalog))
}

pub async fn list_indicators_catalog() -> Json<Vec<IndicatorMeta>> {
    Json(catalog::all())
}
