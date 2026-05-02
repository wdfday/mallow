//! Unified OHLCV + indicator snapshot — `POST /api/data/:symbol`.

use std::collections::HashMap;
use std::sync::Arc;

use alm_ledger::{IndicatorHandle, IndicatorSpec};
use axum::{
    extract::{Path, State},
    http::StatusCode,
    response::{IntoResponse, Response},
    Json,
};
use serde_json::Value;
use tracing::warn;

use super::super::duckdb_helpers as duck;
use super::super::types::{
    CandlesResult, ErrorResponse, IndicatorConfig, IndicatorPoint, UnifiedDataRequest,
    UnifiedDataResponse,
};
use super::super::HttpState;
use super::shared::{clamp_limit, err, paginate, resolve_tf, DEFAULT_LIMIT, MAX_INDICATORS_PER_REQUEST};

// ── POST /api/data/:symbol (unified) ─────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/data/{symbol}",
    params(
        ("symbol" = String, Path, description = "Symbol name e.g. BTCUSDT")
    ),
    request_body = super::super::types::UnifiedDataRequest,
    responses(
        (status = 200, description = "Unified OHLCV + indicator snapshot", body = super::super::types::UnifiedDataResponse),
        (status = 404, description = "Symbol not found", body = super::super::types::ErrorResponse)
    ),
    tag = "live"
)]
pub async fn unified_data(
    State(state): State<HttpState>,
    Path(symbol): Path<String>,
    Json(req): Json<UnifiedDataRequest>,
) -> Response {
    let tf = resolve_tf(&req.tf, state.tf);
    let want_candles = req.candles.is_some();
    let indicator_cfgs = req.indicators.unwrap_or_default();
    if indicator_cfgs.len() > MAX_INDICATORS_PER_REQUEST {
        return err(
            StatusCode::BAD_REQUEST,
            format!("too many indicators: {} > {}", indicator_cfgs.len(), MAX_INDICATORS_PER_REQUEST),
        );
    }

    // Step 1: acquire handles for every requested indicator BEFORE reading any
    // series, so the cells exist + are warmed against the current window.
    let mut handles: Vec<IndicatorHandle> = Vec::with_capacity(indicator_cfgs.len());
    let mut label_for_spec: Vec<(String, IndicatorSpec)> = Vec::with_capacity(indicator_cfgs.len());
    let mut missing: Vec<String> = Vec::new();

    for cfg in &indicator_cfgs {
        let (spec, label) = match build_spec(cfg) {
            Ok(v) => v,
            Err(msg) => {
                warn!(symbol = %symbol, ?tf, error = %msg, "unified_data: bad indicator config");
                missing.push(msg);
                continue;
            }
        };
        match state.ledger.acquire_indicator(&symbol, tf, spec.clone()) {
            Ok(h) => {
                handles.push(h);
                label_for_spec.push((label, spec));
            }
            Err(e) => {
                warn!(symbol = %symbol, ?tf, spec = %spec.canonical_key(), error = %e, "unified_data: acquire_indicator failed");
                missing.push(format!("{}: {}", spec.canonical_key(), e));
            }
        }
    }

    // Step 2: read bars + indicator series under a single read lock.
    let result = state.ledger.with_state(&symbol, tf, |s| {
        let bars_asc: Vec<alm_core::Bar> = s.bar_window.iter().cloned().collect();
        let first = bars_asc.first().map(|b| b.timestamp);

        let candles_out = if let Some(cq) = req.candles.as_ref() {
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
        };

        // Indicators: align to the bar window (no pagination — they are
        // always served in full. Client clips to `candles` range if needed).
        let indicators_out = if !label_for_spec.is_empty() {
            let mut map: HashMap<String, Vec<IndicatorPoint>> = HashMap::new();
            let bar_timestamps: Vec<i64> = bars_asc.iter().map(|b| b.timestamp).collect();
            for (label, spec) in &label_for_spec {
                if let Some(cell) = s.indicators.get(spec) {
                    let series = cell_to_points(cell, &bar_timestamps);
                    map.insert(label.clone(), series);
                }
            }
            Some(map)
        } else {
            None
        };

        (candles_out, indicators_out)
    });

    // Handles drop at end of this function — their cells re-enter the
    // ledger's grace period if no other handle references them. We keep them
    // alive through the lock above; clippy might complain, hence the no-op.
    let _keep_alive = handles;

    let (mut candles_out, indicators_out) = match result {
        Some(pair) => pair,
        None => {
            // No ledger state — if candles were requested with a before cursor,
            // try DuckDB directly so historical data is still accessible even
            // when a symbol is not being tracked live.
            if !want_candles {
                return err(StatusCode::NOT_FOUND, format!("no ledger state for {symbol}"));
            }
            (None, None)
        }
    };

    // DuckDB candle fallback: candle page is empty and a before cursor was given.
    if want_candles {
        let needs_fallback = req.candles.as_ref().and_then(|cq| cq.before).is_some()
            && candles_out.as_ref().map_or(true, |c| c.bars.is_empty());

        if needs_fallback {
            let before_ms = req.candles.as_ref().and_then(|cq| cq.before).unwrap();
            let climit = req.candles.as_ref().map(|cq| clamp_limit(cq.limit, DEFAULT_LIMIT))
                .unwrap_or(DEFAULT_LIMIT);
            let data_dir = Arc::clone(&state.data_dir);
            let parquet_sym = symbol.replace('-', "");
            let tf_str = tf.to_string();

            if let Ok(Ok(bars)) = tokio::task::spawn_blocking(move || {
                duck::query_bars_before(&data_dir, &parquet_sym, &tf_str, before_ms, climit)
            })
            .await
            {
                if !bars.is_empty() {
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
            }
        }
    }

    (
        StatusCode::OK,
        Json(UnifiedDataResponse {
            symbol,
            tf: tf.to_string(),
            candles: if want_candles { candles_out } else { None },
            indicators: indicators_out,
            missing,
        }),
    )
        .into_response()
}

/// Read the ledger cell's columnar series and zip each row against the
/// caller-provided bar timestamps. Rows where every column is `None` (not
/// yet warmed up) are skipped rather than sent as nulls — keeps the JSON
/// tight for charts.
fn cell_to_points(
    cell: &alm_ledger::IndicatorCell,
    bar_timestamps: &[i64],
) -> Vec<IndicatorPoint> {
    let n = cell.len();
    if n == 0 || bar_timestamps.is_empty() {
        return Vec::new();
    }
    // cell rows align to the *last* `n` bars — the ledger maintains this
    // invariant by pushing to the ring on every advance.
    let skip = bar_timestamps.len().saturating_sub(n);
    let ts_slice = &bar_timestamps[skip..];
    let fields = cell.field_names();

    let mut out = Vec::with_capacity(n);
    for i in 0..n.min(ts_slice.len()) {
        let mut row: HashMap<String, f64> = HashMap::with_capacity(fields.len());
        let mut any = false;
        for f in fields {
            if let Some(col) = cell.column(f) {
                if let Some(Some(v)) = col.get(i) {
                    row.insert((*f).to_string(), *v);
                    any = true;
                }
            }
        }
        if any {
            out.push(IndicatorPoint { t: ts_slice[i], fields: row });
        }
    }
    out
}

/// Build an `IndicatorSpec` from a request-level `IndicatorConfig`. Returns
/// `(spec, label)` — the label defaults to the spec's canonical key so chart
/// clients always get something to key their data by.
fn build_spec(cfg: &IndicatorConfig) -> Result<(IndicatorSpec, String), String> {
    let config_value = Value::Object(cfg.config.clone());
    let spec = IndicatorSpec::from_config(config_value, None)
        .map_err(|e| format!("invalid indicator config: {e}"))?;
    let label = cfg.label.clone().unwrap_or_else(|| spec.canonical_key());
    Ok((spec, label))
}
