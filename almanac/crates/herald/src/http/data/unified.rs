//! Unified OHLCV + indicator snapshot — `POST /api/data/:source/:symbol`.

use std::collections::HashMap;
use std::path::Path;
use std::sync::Arc;

use chrono::Utc;

use alm_core::Timeframe;
use alm_indicator::IndicatorBox;
use alm_ledger::{IndicatorHandle, IndicatorSpec};
use axum::{
    extract::{Path as AxumPath, State},
    http::StatusCode,
    response::Response,
    Json,
};
use serde_json::Value;
use tracing::warn;

use super::super::duckdb_helpers as duck;
use super::super::types::{
    ok, BarRecord, CandlesResult, IndicatorConfig, IndicatorPoint, UnifiedDataRequest,
    UnifiedDataResponse,
};
use super::super::HttpState;
use super::shared::{clamp_limit, err, paginate, resolve_tf, DEFAULT_LIMIT, MAX_INDICATORS_PER_REQUEST};

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
        (status = 200, description = "Unified OHLCV + indicator snapshot", body = UnifiedDataResponse),
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
                warn!(source = %source, symbol = %symbol, ?tf, error = %msg, "unified_data: bad indicator config");
                missing.push(msg);
                continue;
            }
        };
        match state.ledger.acquire_indicator(&ledger_key, tf, spec.clone()) {
            Ok(h) => {
                handles.push(h);
                label_for_spec.push((label, spec));
            }
            Err(e) => {
                warn!(source = %source, symbol = %symbol, ?tf, spec = %spec.canonical_key(), error = %e, "unified_data: acquire_indicator failed");
                missing.push(format!("{}: {}", spec.canonical_key(), e));
            }
        }
    }

    // Step 2: read bars + indicator series under a single read lock.
    let result = state.ledger.with_state(&ledger_key, tf, |s| {
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

    // Detect whether `before` falls outside the ledger window.
    let window_start_ms = state.ledger
        .with_state(&ledger_key, tf, |s| s.bar_window.front().map(|b| b.timestamp))
        .flatten();
    let before_ms = req.candles.as_ref().and_then(|cq| cq.before);
    let outside_window = before_ms
        .map(|t| window_start_ms.map_or(true, |ws| t <= ws))
        .unwrap_or(false);
    // Also treat empty ledger (no live bars at all) as "outside window" so
    // an initial load with indicators also falls through to DuckDB compute.
    let ledger_has_bars = window_start_ms.is_some();
    let use_historical = (outside_window || !ledger_has_bars) && want_candles;

    // Historical compute path: before cursor predates the ledger window (or
    // ledger is empty) and indicators were requested. Load warm-up + display
    // bars from DuckDB and run fresh indicator instances.
    if use_historical && !label_for_spec.is_empty() {
        if let (Some(ws), Some(bm)) = (window_start_ms, before_ms) {
            let tf_ms = tf.duration_ms();
            let seam_gap = ws - bm;
            if seam_gap > tf_ms * 2 {
                warn!(
                    source = %source, symbol = %symbol, ?tf,
                    window_start_ms = ws,
                    before_ms = bm,
                    seam_gap_ms = seam_gap,
                    "unified_data: seam between ledger and duckdb compute fallback is wider than 2× tf",
                );
            }
        }
        let before_ms = before_ms.unwrap_or_else(|| Utc::now().timestamp_millis());
        let after_ms      = req.candles.as_ref().and_then(|cq| cq.after);
        let climit        = req.candles.as_ref()
            .map(|cq| clamp_limit(cq.limit, DEFAULT_LIMIT))
            .unwrap_or(DEFAULT_LIMIT);
        let data_dir      = Arc::clone(&state.data_dir);
        let parquet_sym   = parquet_sym.clone();
        let specs_owned   = label_for_spec.clone();

        let hist = tokio::task::spawn_blocking(move || {
            compute_historical(&data_dir, &parquet_sym, tf, &specs_owned, before_ms, after_ms, climit)
        })
        .await
        .unwrap_or_else(|_| (vec![], HashMap::new()));

        let (hist_bars, hist_inds) = hist;
        let next_before = hist_bars.first().map(|b| b.t);
        let next_after  = hist_bars.last().map(|b| b.t);
        let candles_out = Some(CandlesResult {
            count: hist_bars.len(),
            bars: hist_bars,
            next_before,
            next_after,
            truncated_below: false,
        });
        return ok(UnifiedDataResponse {
            source,
            symbol,
            tf: tf.to_string(),
            candles: candles_out,
            indicators: Some(hist_inds),
            missing,
        });
    }

    let (mut candles_out, indicators_out) = match result {
        Some(pair) => pair,
        None => {
            if !want_candles {
                return err(StatusCode::NOT_FOUND, format!("no ledger state for {ledger_key}"));
            }
            (None, None)
        }
    };

    // Seam diagnostic for candle-only path: if `before` is just outside the
    // ledger window, the response will mix ledger bars (none here, since
    // outside_window=true) with DuckDB bars in subsequent pages. Log the seam.
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

    // DuckDB candle-only fallback (no indicators requested).
    // Triggers when:
    //   (a) explicit `before` cursor but ledger returned nothing for that range, OR
    //   (b) initial load (no `before`, no `after`) but ledger window is empty / missing.
    // Does NOT trigger for `after`-cursor requests (live tail of recent bars).
    if want_candles && label_for_spec.is_empty() {
        let after_ms = req.candles.as_ref().and_then(|cq| cq.after);
        let ledger_empty = candles_out.as_ref().map_or(true, |c| c.bars.is_empty());
        let needs_fallback = after_ms.is_none() && ledger_empty;

        if needs_fallback {
            // Use the explicit `before` cursor, or fall back to "now" for initial loads.
            let bms = before_ms.unwrap_or_else(|| Utc::now().timestamp_millis());
            let climit    = req.candles.as_ref()
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
                    // Empty result or spawn error — no more history available.
                    if let Some(ref mut c) = candles_out {
                        c.truncated_below = false;
                    }
                }
            }
        }
    }

    ok(UnifiedDataResponse {
        source,
        symbol,
        tf: tf.to_string(),
        candles: if want_candles { candles_out } else { None },
        indicators: indicators_out,
        missing,
    })
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

/// Compute indicator values on historical bars outside the live ledger window.
///
/// Loads warm-up + display bars from parquet, feeds them through fresh
/// `IndicatorBox` instances (no ledger involved), then returns only the
/// display window bars with their indicator values.
///
/// When `after_ms` is `Some(t)`, the display window is `[t, before_ms)` and
/// warm-up bars are loaded before `t`. When `after_ms` is `None`, the
/// display window is the last `limit` bars before `before_ms`.
///
/// `parquet_symbol` is the on-disk name (no dashes); `label_for_spec` is the
/// same list built by `unified_data` — `(label, IndicatorSpec)` pairs.
fn compute_historical(
    data_dir: &Path,
    parquet_symbol: &str,
    tf: Timeframe,
    label_for_spec: &[(String, IndicatorSpec)],
    before_ms: i64,
    after_ms: Option<i64>,
    limit: usize,
) -> (Vec<BarRecord>, HashMap<String, Vec<IndicatorPoint>>) {
    let max_warm: usize = label_for_spec
        .iter()
        .map(|(_, s)| s.warm_estimate())
        .max()
        .unwrap_or(0);

    let tf_ms = tf.duration_ms();
    let from_ms = match after_ms {
        Some(after) => after.saturating_sub(max_warm as i64 * tf_ms),
        None        => before_ms.saturating_sub((max_warm + limit) as i64 * tf_ms),
    };
    // Total bar count: warm-up + display. For ranged queries, add a generous
    // buffer so DuckDB doesn't need to be re-queried for warm-up misses.
    let total = match after_ms {
        Some(after) => {
            let range_bars = ((before_ms - after) / tf_ms.max(1)) as usize;
            max_warm + range_bars + limit
        }
        None => max_warm + limit,
    };
    let live_sym = parquet_symbol;

    let bars = match duck::query_bars_for_compute(
        data_dir, parquet_symbol, live_sym, &tf.to_string(), from_ms, before_ms, total,
    ) {
        Ok(b) => b,
        Err(_) => return (vec![], HashMap::new()),
    };

    if bars.is_empty() {
        return (vec![], HashMap::new());
    }

    // Build one fresh IndicatorBox per spec.
    let mut boxes: Vec<(String, IndicatorBox)> = label_for_spec
        .iter()
        .filter_map(|(label, spec)| {
            IndicatorBox::from_config(&spec.config)
                .ok()
                .map(|b| (label.clone(), b))
        })
        .collect();

    // Feed all bars through; accumulate per-bar field maps for each indicator.
    let n = bars.len();
    let mut series: HashMap<String, Vec<(i64, HashMap<String, f64>)>> =
        boxes.iter().map(|(l, _)| (l.clone(), Vec::with_capacity(n))).collect();

    for bar in &bars {
        for (label, ind) in &mut boxes {
            if let Some(fields) = ind.update(bar) {
                series.entry(label.clone()).or_default().push((bar.timestamp, fields));
            }
        }
    }

    // Determine the display window.
    let display_bars: Vec<BarRecord> = match after_ms {
        Some(after) => bars.iter()
            .filter(|b| b.timestamp >= after)
            .map(BarRecord::from)
            .collect(),
        None => {
            let display_start = bars.len().saturating_sub(limit);
            bars[display_start..].iter().map(BarRecord::from).collect()
        }
    };

    if display_bars.is_empty() {
        return (vec![], HashMap::new());
    }
    let win_start = display_bars.first().map(|b| b.t).unwrap_or(0);

    // Filter indicator series to the display window.
    let indicators: HashMap<String, Vec<IndicatorPoint>> = series
        .into_iter()
        .map(|(label, pts)| {
            let clipped = pts
                .into_iter()
                .filter(|(t, _)| *t >= win_start)
                .map(|(t, fields)| IndicatorPoint { t, fields })
                .collect();
            (label, clipped)
        })
        .collect();

    (display_bars, indicators)
}
