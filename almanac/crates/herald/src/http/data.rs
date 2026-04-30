//! OHLCV + unified data handlers.
//!
//! All three endpoints read directly from the ledger's `bar_window` — no
//! Parquet / disk IO on the request path. When a requested range falls
//! outside the current window the handler returns what it has plus a
//! `truncated_below` flag; walk-back LRU (future) will transparently extend
//! coverage without changing the wire contract.

use std::collections::HashMap;
use std::sync::Arc;

use alm_core::Timeframe;
use alm_ledger::{IndicatorHandle, IndicatorSpec, Ledger};
use axum::{
    extract::{Path, Query, State},
    http::StatusCode,
    response::{IntoResponse, Response},
    routing::get,
    Json, Router,
};
use serde_json::Value;
use tracing::{debug, warn};

use super::types::{
    BarRecord, CandlesResult, DataQuery, DataResponse, ErrorResponse, IndicatorConfig,
    IndicatorPoint, LatestQuery, UnifiedDataRequest, UnifiedDataResponse,
};
use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/data/{symbol}", get(get_data).post(unified_data))
        .route("/api/data/{symbol}/latest", get(get_latest))
}

// ── Limits ────────────────────────────────────────────────────────────────────

const DEFAULT_LIMIT: usize = 500;
const MAX_LIMIT: usize = 5_000;
const MAX_INDICATORS_PER_REQUEST: usize = 16;

// ── Shared paging helper ──────────────────────────────────────────────────────

/// Paging result over a slice of bars sorted by timestamp ascending.
struct Page {
    bars: Vec<BarRecord>,
    next_before: Option<i64>,
    next_after: Option<i64>,
    truncated_below: bool,
}

/// Apply the three supported cursor modes (`before`, `after`, neither)
/// against `bars` which must be sorted ascending by `t`.
///
/// - `before`: newest-first page of bars with `t < before`, trimmed to `limit`.
///   The response itself stays oldest-first so the chart can concatenate pages
///   without resorting.
/// - `after`:  oldest-first page of bars with `t > after`, trimmed to `limit`.
/// - neither:  the last `limit` bars.
///
/// `window_starts_at` is the timestamp of the oldest bar still in the ledger
/// window — used to set `truncated_below` so the client knows whether to
/// issue a walk-back / cold-storage request for older history.
fn paginate(
    bars_asc: &[alm_core::Bar],
    before: Option<i64>,
    after: Option<i64>,
    limit: usize,
    window_starts_at: Option<i64>,
) -> Page {
    // Fast path: empty ledger window.
    if bars_asc.is_empty() {
        return Page {
            bars: Vec::new(),
            next_before: None,
            next_after: None,
            truncated_below: before.is_some() && window_starts_at.is_none(),
        };
    }

    let first_t = bars_asc.first().map(|b| b.timestamp).unwrap_or(0);

    // Select the raw range according to the cursor.
    let range: Vec<&alm_core::Bar> = match (before, after) {
        (Some(cut), _) => {
            // Bars with t < cut, take newest `limit` of those.
            let mut hits: Vec<&alm_core::Bar> =
                bars_asc.iter().filter(|b| b.timestamp < cut).collect();
            if hits.len() > limit {
                let skip = hits.len() - limit;
                hits.drain(..skip);
            }
            hits
        }
        (None, Some(start)) => bars_asc
            .iter()
            .filter(|b| b.timestamp > start)
            .take(limit)
            .collect(),
        (None, None) => {
            // Last `limit` bars.
            let skip = bars_asc.len().saturating_sub(limit);
            bars_asc.iter().skip(skip).collect()
        }
    };

    let records: Vec<BarRecord> = range.iter().map(|b| BarRecord::from(*b)).collect();

    // Cursor hints for the next request. `next_before` is the oldest returned
    // bar's timestamp (the client should pass it straight back to scroll into
    // the past). `next_after` is the newest returned bar's timestamp.
    let next_before = records.first().map(|r| r.t);
    let next_after = records.last().map(|r| r.t);

    // Signal that older bars exist beyond the ledger window.
    let truncated_below = match before {
        Some(cut) => {
            // The client asked for bars older than `cut` but our window
            // starts at `first_t` — if `cut <= first_t` we gave them
            // nothing in-window and the rest lives in cold storage.
            cut <= first_t && window_starts_at.is_some()
        }
        None if after.is_none() => {
            // Last-N page. Truncation only matters if the client paged
            // backwards beyond `first_t`, so it is surfaced via `next_before`.
            false
        }
        _ => false,
    };

    Page {
        bars: records,
        next_before,
        next_after,
        truncated_below,
    }
}

fn clamp_limit(raw: Option<usize>, default: usize) -> usize {
    raw.unwrap_or(default).clamp(1, MAX_LIMIT)
}

fn resolve_tf(raw: &Option<String>, default: Timeframe) -> Timeframe {
    raw.as_deref()
        .and_then(parse_tf)
        .unwrap_or(default)
}

fn parse_tf(s: &str) -> Option<Timeframe> {
    match s.to_ascii_uppercase().as_str() {
        "M1"  => Some(Timeframe::M1),
        "M5"  => Some(Timeframe::M5),
        "M15" => Some(Timeframe::M15),
        "M30" => Some(Timeframe::M30),
        "H1"  => Some(Timeframe::H1),
        "H4"  => Some(Timeframe::H4),
        "D1"  => Some(Timeframe::D1),
        "W1"  => Some(Timeframe::W1),
        _ => None,
    }
}

fn err(status: StatusCode, msg: impl Into<String>) -> Response {
    (status, Json(ErrorResponse::new(msg))).into_response()
}

// ── GET /api/data/:symbol ─────────────────────────────────────────────────────

pub async fn get_data(
    State(state): State<HttpState>,
    Path(symbol): Path<String>,
    Query(q): Query<DataQuery>,
) -> Response {
    let tf = resolve_tf(&q.tf, state.tf);
    let limit = clamp_limit(q.limit, DEFAULT_LIMIT);

    let page = match state.ledger.with_state(&symbol, tf, |s| {
        let bars_asc: Vec<alm_core::Bar> = s.bar_window.iter().cloned().collect();
        let first = bars_asc.first().map(|b| b.timestamp);
        paginate(&bars_asc, q.before, q.after, limit, first)
    }) {
        Some(p) => p,
        None => {
            debug!(symbol = %symbol, ?tf, "get_data: no ledger state yet");
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

// ── GET /api/data/:symbol/latest ──────────────────────────────────────────────

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

// ── POST /api/data/:symbol (unified) ─────────────────────────────────────────

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

    match result {
        Some((candles, indicators)) => (
            StatusCode::OK,
            Json(UnifiedDataResponse {
                symbol,
                tf: tf.to_string(),
                candles: if want_candles { candles } else { None },
                indicators,
                missing,
            }),
        )
            .into_response(),
        None => err(StatusCode::NOT_FOUND, format!("no ledger state for {symbol}")),
    }
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
