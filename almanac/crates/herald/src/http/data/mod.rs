//! Data sub-module: OHLCV, unified, and DuckDB handlers.

pub mod duckdb;
pub mod ohlcv;
pub mod shared;
pub mod unified;

pub use duckdb::query;
pub use ohlcv::{get_data, get_latest};
pub use unified::unified_data;

use axum::{routing::get, Router};
use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/data/:symbol", get(get_data).post(unified_data))
        .route("/api/data/:symbol/latest", get(get_latest))
        .merge(duckdb::routes())
}
