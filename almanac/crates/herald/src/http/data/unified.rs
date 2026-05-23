//! Unified OHLCV snapshot — `POST /api/data/:source/:symbol`.
//!
//! Returns raw bars only. Indicator computation is handled client-side via WASM.

use std::sync::Arc;

use chrono::Utc;

use axum::{
    extract::{Path as AxumPath, State},
    http::StatusCode,
    response::Response,
    Json,
};
use tracing::warn;

use super::super::duckdb_helpers as duck;
use super::super::types::{
    ok, CandlesResult, UnifiedDataRequest, UnifiedDataResponse,
};
use super::super::HttpState;
use super::shared::{clamp_limit, err, paginate, resolve_tf, DEFAULT_LIMIT};

const VALID_SOURCES: &[&str] = &["binance", "okx", "bybit"];

// ── POST /api/data/:source/:symbol (unified) ──────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/data/{source}/{symbol}",
    params(
        ("source" = String, Path, description = "Exchange source: binance | okx | bybit"),
        ("symbol" = String, Path, description = "Symbol ticker e.g. BTCUSDT, BTC-USDT")
    ),
    request_body = UnifiedDataRequest,
    responses(
        (status = 200, description = "OHLCV bars", body = UnifiedDataResponse),
        (status = 400, description = "Unknown source"),
        (status = 404, description = "Symbol not found in ledger")
    ),
    tag = "live"
)]
pub async fn unified_data(
    State(state): State<HttpState>,
    AxumPath((source, symbol)): AxumPath<(String, String)>,
    Json(req): Json<UnifiedDataRequest>,
) -> Response {
    let source = source.to_lowercase();
    if !VALID_SOURCES.contains(&source.as_str()) {
        return err(
            StatusCode::BAD_REQUEST,
            format!("unknown source '{}'; expected one of: {}", source, VALID_SOURCES.join(", ")),
        );
    }
    let ledger_key = format!("{}:{}", source, symbol);
    let parquet_sym = symbol.replace('-', "");
    let tf = resolve_tf(&req.tf, state.tf);
    let want_candles = req.candles.is_some();

    // Read bars from ledger.
    let result = state.ledger.with_state(&ledger_key, tf, |s| {
        let bars_asc: Vec<alm_core::Bar> = s.bar_window.iter().cloned().collect();
        let first = bars_asc.first().map(|b| b.timestamp);

        if let Some(cq) = req.candles.as_ref() {
            let limit = clamp_limit(cq.limit, DEFAULT_LIMIT);
            let page = paginate(&bars_asc, cq.before, cq.after, limit, first);
            Some(CandlesResult {
                count: page.bars.len(),
                bars: page.bars,
                next_before: page.next_before,
                next_after: page.next_after,
                truncated_below: page.truncated_below,
            })
        } else {
            None
        }
    });

    let window_start_ms = state.ledger
        .with_state(&ledger_key, tf, |s| s.bar_window.front().map(|b| b.timestamp))
        .flatten();
    let before_ms = req.candles.as_ref().and_then(|cq| cq.before);
    let after_ms  = req.candles.as_ref().and_then(|cq| cq.after);

    let outside_window = before_ms
        .map(|t| window_start_ms.map_or(true, |ws| t <= ws))
        .unwrap_or(false);
    let ledger_has_bars = window_start_ms.is_some();

    // Seam diagnostic.
    if let (Some(ws), Some(bm)) = (window_start_ms, before_ms) {
        let tf_ms = tf.duration_ms();
        if bm <= ws {
            let seam_gap = ws - bm;
            if seam_gap > tf_ms * 2 {
                warn!(
                    source = %source, symbol = %symbol, ?tf,
                    window_start_ms = ws,
                    before_ms = bm,
                    seam_gap_ms = seam_gap,
                    "unified_data: ledger↔duckdb seam wider than 2× tf",
                );
            }
        }
    }

    let has_ledger_state = result.is_some();
    let mut candles_out = result.flatten();

    // DuckDB fallback: triggers when bars are not available from the live ledger.
    // Cases:
    //   (a) `before` cursor predates the ledger window, OR
    //   (b) initial load (no cursor) and ledger is empty / missing.
    // Does NOT trigger for `after`-cursor requests (live tail).
    if want_candles {
        let ledger_empty = candles_out.as_ref().map_or(true, |c| c.bars.is_empty());
        let needs_fallback = after_ms.is_none() && (ledger_empty || outside_window && !ledger_has_bars);

        if needs_fallback {
            let bms = before_ms.unwrap_or_else(|| Utc::now().timestamp_millis());
            let climit = req.candles.as_ref()
                .map(|cq| clamp_limit(cq.limit, DEFAULT_LIMIT))
                .unwrap_or(DEFAULT_LIMIT);
            let data_dir    = Arc::clone(&state.data_dir);
            let parquet_sym = parquet_sym.clone();
            let tf_str      = tf.to_string();
            let log_sym     = parquet_sym.clone();
            let log_tf      = tf_str.clone();

            let duck_result = tokio::task::spawn_blocking(move || {
                duck::query_bars_before(&data_dir, &parquet_sym, &tf_str, bms, climit)
            })
            .await;

            match duck_result {
                Ok(Ok(bars)) if !bars.is_empty() => {
                    let next_before = bars.first().map(|b| b.t);
                    let next_after  = bars.last().map(|b| b.t);
                    candles_out = Some(CandlesResult {
                        count: bars.len(),
                        bars,
                        next_before,
                        next_after,
                        truncated_below: false,
                    });
                }
                Ok(Err(ref e)) => {
                    warn!(symbol = %log_sym, tf = %log_tf, error = %e, "duckdb candle fallback error");
                    if let Some(ref mut c) = candles_out {
                        c.truncated_below = false;
                    }
                }
                _ => {
                    if let Some(ref mut c) = candles_out {
                        c.truncated_below = false;
                    }
                }
            }
        }
    }

    if !want_candles && !has_ledger_state {
        return err(StatusCode::NOT_FOUND, format!("no ledger state for {ledger_key}"));
    }

    ok(UnifiedDataResponse {
        source,
        symbol,
        tf: tf.to_string(),
        candles: if want_candles { candles_out } else { None },
    })
}
