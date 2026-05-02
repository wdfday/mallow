//! OHLCV data handlers — `GET /api/data/:symbol` and `GET /api/data/:symbol/latest`.
//!
//! Primary source: ledger `bar_window` (in-memory ring buffer, ~24 h).
//!
//! Parquet fallback: when a `before` cursor falls outside the ring (page empty)
//! or the symbol has no ledger state, `GET /api/data/:symbol?before=<ts>` and
//! the `POST` unified endpoint transparently fall back to a DuckDB query against
//! the flat Parquet files in `data_dir`. The response shape is identical —
//! callers do not need to handle the switch.

use std::sync::Arc;

use alm_core::Timeframe;
use alm_ledger::Ledger;
use axum::{
    extract::{Path, Query, State},
    http::StatusCode,
    response::{IntoResponse, Response},
    Json,
};
use tracing::{debug, warn};

use super::super::duckdb_helpers as duck;
use super::super::types::{BarRecord, DataQuery, DataResponse, ErrorResponse, LatestQuery};
use super::super::HttpState;
use super::shared::{clamp_limit, resolve_tf, DEFAULT_LIMIT};
pub use super::shared::{err, paginate, Page};

// ── GET /api/data/:symbol ─────────────────────────────────────────────────────

#[utoipa::path(
    get,
    path = "/api/data/{symbol}",
    params(
        ("symbol" = String, Path, description = "Symbol name e.g. BTCUSDT"),
        super::super::types::DataQuery
    ),
    responses(
        (status = 200, description = "OHLCV bars with cursor pagination", body = super::super::types::DataResponse),
        (status = 404, description = "Symbol not found", body = super::super::types::ErrorResponse)
    ),
    tag = "live"
)]
pub async fn get_data(
    State(state): State<HttpState>,
    Path(symbol): Path<String>,
    Query(q): Query<DataQuery>,
) -> Response {
    let tf = resolve_tf(&q.tf, state.tf);
    let limit = clamp_limit(q.limit, DEFAULT_LIMIT);

    // Try ring buffer first.
    let ring_page = state.ledger.with_state(&symbol, tf, |s| {
        let bars_asc: Vec<alm_core::Bar> = s.bar_window.iter().cloned().collect();
        let first = bars_asc.first().map(|b| b.timestamp);
        paginate(&bars_asc, q.before, q.after, limit, first)
    });

    // DuckDB fallback: triggered when scrolling back beyond ring coverage.
    //   - before cursor with empty ring result (cursor predates ring window)
    //   - OR no ledger state at all (symbol not tracked live) + before cursor given
    let needs_parquet_fallback = q.before.is_some()
        && ring_page.as_ref().map_or(true, |p| p.bars.is_empty());

    let page = if needs_parquet_fallback {
        let data_dir = Arc::clone(&state.data_dir);
        // OKX symbols use dashes ("BTC-USDT"); parquet dirs use no dashes ("BTCUSDT").
        let parquet_sym = symbol.replace('-', "");
        let tf_str = tf.to_string();
        let before_ms = q.before.unwrap();

        match tokio::task::spawn_blocking(move || {
            duck::query_bars_before(&data_dir, &parquet_sym, &tf_str, before_ms, limit)
        })
        .await
        {
            Ok(Ok(bars)) if !bars.is_empty() => {
                let next_before = bars.first().map(|b| b.t);
                let next_after  = bars.last().map(|b| b.t);
                Page { bars, next_before, next_after, truncated_below: false }
            }
            Ok(Ok(_)) => {
                // Parquet also empty → return whatever ring had (may be empty).
                ring_page.unwrap_or(Page {
                    bars: vec![], next_before: None, next_after: None, truncated_below: false,
                })
            }
            Ok(Err(e)) => {
                debug!(symbol = %symbol, error = %e, "duckdb parquet fallback failed");
                ring_page.unwrap_or(Page {
                    bars: vec![], next_before: None, next_after: None, truncated_below: false,
                })
            }
            Err(e) => {
                warn!(symbol = %symbol, error = %e, "duckdb fallback task panicked");
                ring_page.unwrap_or(Page {
                    bars: vec![], next_before: None, next_after: None, truncated_below: false,
                })
            }
        }
    } else if let Some(p) = ring_page {
        p
    } else {
        // No ledger state and no before cursor.
        debug!(symbol = %symbol, ?tf, "get_data: no ledger state");
        return err(StatusCode::NOT_FOUND, format!("no ledger state for {symbol}"));
    };

    (
        StatusCode::OK,
        Json(DataResponse {
            symbol,
            tf: tf.to_string(),
            count: page.bars.len(),
            bars: page.bars,
            next_before: page.next_before,
            next_after: page.next_after,
            truncated_below: page.truncated_below,
        }),
    )
        .into_response()
}

// ── GET /api/data/:symbol/latest ──────────────────────────────────────────────

#[utoipa::path(
    get,
    path = "/api/data/{symbol}/latest",
    params(
        ("symbol" = String, Path, description = "Symbol name e.g. BTCUSDT"),
        super::super::types::LatestQuery
    ),
    responses(
        (status = 200, description = "Last N bars", body = super::super::types::DataResponse),
        (status = 404, description = "Symbol not found", body = super::super::types::ErrorResponse)
    ),
    tag = "live"
)]
pub async fn get_latest(
    State(state): State<HttpState>,
    Path(symbol): Path<String>,
    Query(q): Query<LatestQuery>,
) -> Response {
    let tf = resolve_tf(&q.tf, state.tf);
    let limit = clamp_limit(q.n, DEFAULT_LIMIT);

    let page = match state.ledger.with_state(&symbol, tf, |s| {
        let bars_asc: Vec<alm_core::Bar> = s.bar_window.iter().cloned().collect();
        let first = bars_asc.first().map(|b| b.timestamp);
        paginate(&bars_asc, None, None, limit, first)
    }) {
        Some(p) => p,
        None => {
            return err(StatusCode::NOT_FOUND, format!("no ledger state for {symbol}"));
        }
    };

    (
        StatusCode::OK,
        Json(DataResponse {
            symbol,
            tf: tf.to_string(),
            count: page.bars.len(),
            bars: page.bars,
            next_before: page.next_before,
            next_after: page.next_after,
            truncated_below: page.truncated_below,
        }),
    )
        .into_response()
}

// Keep the module callable from tests without pulling axum into scope.
#[allow(dead_code)]
pub(crate) fn __paginate_for_test(
    bars_asc: &[alm_core::Bar],
    before: Option<i64>,
    after: Option<i64>,
    limit: usize,
) -> (Vec<i64>, Option<i64>, Option<i64>, bool) {
    let first = bars_asc.first().map(|b| b.timestamp);
    let p = paginate(bars_asc, before, after, limit, first);
    let ts = p.bars.iter().map(|b| b.t).collect();
    (ts, p.next_before, p.next_after, p.truncated_below)
}

// Silence the "unused" warning on `Ledger` when features change.
#[allow(dead_code)]
fn _assert_ledger_use(_l: &Arc<Ledger>) {}
