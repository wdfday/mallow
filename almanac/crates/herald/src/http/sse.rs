//! Server-Sent Events — real-time push for bars and signals.
//!
//! ```text
//! POST /api/stream/:symbol   → bar events per symbol  (event: "bar")
//! GET  /api/stream/signals   → all signal batches      (event: "signal")
//! ```
//!
//! # Stream modes
//!
//! `POST /api/stream/:symbol` supports two mutually exclusive modes:
//!
//! **Structured** (`indicators` field): specify indicator configs in JSON.
//! The server reads the latest cell values from the ledger each bar.
//!
//! **Rhai** (`script` field): write a Rhai script using `ind.TYPE(period)`
//! declarations and `plot("name", value)` calls. The server runs the script
//! per bar and returns exactly the plotted values.
//!
//! # Status event
//!
//! Both modes emit a `status` event on connect reporting warm-up state per
//! indicator. `all_ready: false` means some indicators need more live bars
//! to converge — the stream flows but those indicators may be absent from
//! early events.

use std::collections::{HashMap, VecDeque};
use std::convert::Infallible;
use std::sync::Arc;
use std::time::Duration;

use alm_core::Bar;
use alm_ledger::{IndicatorCell, IndicatorHandle, IndicatorSpec};
use alm_strategy::RhaiStreamEval;
use axum::{
    extract::{Path, State},
    response::sse::{Event, KeepAlive, Sse},
    routing::{get, post},
    Json, Router,
};
use futures::{stream::BoxStream, Stream};
use serde_json::Value;
use tokio_stream::wrappers::BroadcastStream;
use tokio_stream::StreamExt as TokioStreamExt;

use super::data::shared::resolve_tf;
use super::types::{BarStreamEvent, IndicatorConfig, IndicatorStatus, StreamRequest, StreamStatus};
use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/stream/signals", get(stream_signals))
        .route("/api/stream/:symbol", post(stream_bars))
}

// ── POST /api/stream/:symbol ──────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/stream/{symbol}",
    params(("symbol" = String, Path, description = "Symbol e.g. BTCUSDT")),
    request_body = StreamRequest,
    responses(
        (status = 200, description = "SSE bar+indicator stream (text/event-stream)")
    ),
    tag = "stream"
)]
pub async fn stream_bars(
    State(state): State<HttpState>,
    Path(symbol): Path<String>,
    Json(req): Json<StreamRequest>,
) -> Sse<BoxStream<'static, Result<Event, Infallible>>> {
    let tf  = resolve_tf(&req.tf, state.tf);
    let sym = symbol.to_uppercase();

    // ── Parse script / structured indicators ─────────────────────────────────

    // (var_name/label, IndicatorSpec, field, buf_depth)
    type DeclEntry = (String, IndicatorSpec, String, usize);

    let (evaluator, decl_entries): (Option<RhaiStreamEval>, Vec<DeclEntry>) =
        if let Some(script) = &req.script {
            match RhaiStreamEval::from_script(script) {
                Ok(ev) => {
                    let entries = ev.decls().iter().filter_map(|d| {
                        let spec = IndicatorSpec::from_config(
                            d.spec_config.clone(), d.source_tf,
                        ).ok()?;
                        Some((d.var_name.clone(), spec, d.field.clone(), d.buf_depth))
                    }).collect();
                    (Some(ev), entries)
                }
                Err(e) => {
                    tracing::warn!(error = %e, "RhaiStreamEval compile failed");
                    (None, vec![])
                }
            }
        } else {
            let entries = req.indicators.unwrap_or_default().iter().filter_map(|cfg| {
                let (spec, label) = build_spec(cfg).ok()?;
                let field = "value".to_string();
                Some((label, spec, field, 1usize))
            }).collect();
            (None, entries)
        };

    // ── Acquire ledger handles ────────────────────────────────────────────────

    let mut handles: Vec<IndicatorHandle> = Vec::with_capacity(decl_entries.len());
    let mut label_specs: Vec<(String, IndicatorSpec)> = Vec::with_capacity(decl_entries.len());
    for (label, spec, _, _) in &decl_entries {
        if let Ok(h) = state.ledger.acquire_indicator(&sym, tf, spec.clone()) {
            handles.push(h);
            label_specs.push((label.clone(), spec.clone()));
        }
    }

    // ── Status event ─────────────────────────────────────────────────────────

    let status       = build_status(&state, &sym, tf, &label_specs);
    let status_event = status_sse_event(&status);

    // ── Per-bar stream ────────────────────────────────────────────────────────

    let ledger        = Arc::clone(&state.ledger);
    let rx            = state.bar_bcast.subscribe();
    let bar_buf_depth = evaluator.as_ref().map(|e| e.bar_buf_depth()).unwrap_or(2);
    let mut bar_history: VecDeque<Bar> = VecDeque::with_capacity(bar_buf_depth);

    let bar_stream = TokioStreamExt::filter_map(BroadcastStream::new(rx), move |res| {
        let _ = &handles;
        match res {
            Ok(bar) if bar.symbol.to_uppercase() == sym => {
                bar_history.push_front(bar.clone());
                if bar_history.len() > bar_buf_depth { bar_history.pop_back(); }

                let indicators: HashMap<String, HashMap<String, f64>> =
                    if let Some(ev) = &evaluator {
                        // Rhai mode: read arrays from ledger, run script, collect plot().
                        let ind_arrays = ledger.with_state(&sym, tf, |s| {
                            let mut map: HashMap<String, Vec<f64>> = HashMap::new();
                            for (var_name, spec, field, buf) in &decl_entries {
                                if let Some(cell) = s.indicators.get(spec) {
                                    map.insert(var_name.clone(), cell_last_n(cell, field, *buf));
                                }
                            }
                            map
                        }).unwrap_or_default();
                        let history: Vec<Bar> = bar_history.iter().cloned().collect();
                        ev.run(&ind_arrays, &history)
                            .into_iter()
                            .map(|(k, v)| (k, HashMap::from([("value".to_string(), v)])))
                            .collect()
                    } else {
                        // Structured mode: read last cell values from ledger.
                        ledger.with_state(&sym, tf, |s| {
                            let mut map = HashMap::new();
                            for (label, spec, _, _) in &decl_entries {
                                if let Some(cell) = s.indicators.get(spec) {
                                    let row = cell_last_fields(cell);
                                    if !row.is_empty() { map.insert(label.clone(), row); }
                                }
                            }
                            map
                        }).unwrap_or_default()
                    };

                Some(Ok(bar_sse_event(&bar, indicators)))
            }
            _ => None,
        }
    });

    let stream: BoxStream<'static, Result<Event, Infallible>> = Box::pin(
        TokioStreamExt::chain(futures::stream::once(async move { Ok(status_event) }), bar_stream),
    );

    Sse::new(stream).keep_alive(KeepAlive::new().interval(Duration::from_secs(15)).text("ping"))
}

// ── GET /api/stream/signals ───────────────────────────────────────────────────

#[utoipa::path(
    get,
    path = "/api/stream/signals",
    responses(
        (status = 200, description = "SSE signal stream (text/event-stream, event: signal)")
    ),
    tag = "stream"
)]
pub async fn stream_signals(
    State(state): State<HttpState>,
) -> Sse<impl Stream<Item = Result<Event, Infallible>>> {
    let rx = state.sig_bcast.subscribe();
    let stream = TokioStreamExt::filter_map(BroadcastStream::new(rx), |res| match res {
        Ok(batch) => {
            let data = serde_json::to_string(&*batch).unwrap_or_default();
            Some(Ok(Event::default().event("signal").data(data)))
        }
        _ => None,
    });
    Sse::new(stream).keep_alive(KeepAlive::new().interval(Duration::from_secs(15)).text("ping"))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn build_status(
    state: &HttpState,
    sym: &str,
    tf: alm_core::Timeframe,
    label_specs: &[(String, IndicatorSpec)],
) -> StreamStatus {
    let (bars_available, ind_statuses) = state.ledger.with_state(sym, tf, |s| {
        let bars_available = s.bar_window.len();
        let mut map = HashMap::new();
        for (label, spec) in label_specs {
            let warm_estimate  = spec.warm_estimate();
            let cell_ready     = s.indicators.get(spec).and_then(|c| c.ready_since_t).is_some();
            let ready          = cell_ready && bars_available >= warm_estimate;
            let bars_needed    = if ready { 0 } else { warm_estimate.saturating_sub(bars_available) };
            map.insert(label.clone(), IndicatorStatus {
                ready, warm_estimate, bars_available, bars_needed,
            });
        }
        (bars_available, map)
    }).unwrap_or_else(|| (0, HashMap::new()));

    let all_ready = ind_statuses.values().all(|s| s.ready);
    StreamStatus { bars_available, all_ready, indicators: ind_statuses }
}

fn status_sse_event(status: &StreamStatus) -> Event {
    let data = serde_json::to_string(status).unwrap_or_default();
    Event::default().event("status").data(data)
}

fn bar_sse_event(bar: &Bar, indicators: HashMap<String, HashMap<String, f64>>) -> Event {
    let event = BarStreamEvent {
        t: bar.timestamp, o: bar.open,  h: bar.high,
        l: bar.low,       c: bar.close, v: bar.volume,
        indicators,
    };
    Event::default().event("bar").data(serde_json::to_string(&event).unwrap_or_default())
}

fn cell_last_fields(cell: &IndicatorCell) -> HashMap<String, f64> {
    let mut row = HashMap::new();
    for f in cell.field_names() {
        if let Some(col) = cell.column(f) {
            if let Some(Some(v)) = col.back() { row.insert((*f).to_string(), *v); }
        }
    }
    row
}

fn cell_last_n(cell: &IndicatorCell, field: &str, n: usize) -> Vec<f64> {
    cell.column(field)
        .map(|col| col.iter().rev().take(n).filter_map(|v| *v).collect())
        .unwrap_or_default()
}

fn build_spec(cfg: &IndicatorConfig) -> Result<(IndicatorSpec, String), String> {
    let spec  = IndicatorSpec::from_config(Value::Object(cfg.config.clone()), None)
        .map_err(|e| format!("invalid indicator config: {e}"))?;
    let label = cfg.label.clone().unwrap_or_else(|| spec.canonical_key());
    Ok((spec, label))
}
