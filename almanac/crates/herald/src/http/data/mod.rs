//! Data sub-module: unified OHLCV + indicator snapshot.

pub mod shared;
pub mod unified;

pub use unified::unified_data;

use axum::{routing::post, Router};
use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/data/:symbol", post(unified_data))
}
