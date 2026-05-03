//! Server-Sent Events — real-time push for bars and signals.
//!
//! ```text
//! POST /api/stream/:symbol   → bar + indicator events per symbol  (event: "bar")
//! GET  /api/stream/signals   → all signal batches                  (event: "signal")
//! ```
//!
//! `POST /api/stream/:symbol` accepts a JSON body with an optional `tf` and
//! `indicators` list (same shape as `POST /api/data/:symbol`). Each SSE event
//! carries the new OHLCV bar plus the current value of every requested
//! indicator, so a chart only needs a single connection for live updates.
//!
//! The signals endpoint remains GET — no body needed, it fans out all signals.
//!
//! Both streams send a keep-alive comment every 15 s so proxies do not close
//! idle connections.

use std::collections::HashMap;
use std::convert::Infallible;
use std::sync::Arc;
use std::time::Duration;

use alm_ledger::{IndicatorCell, IndicatorHandle, IndicatorSpec};
use axum::{
    extract::{Path, State},
    response::sse::{Event, KeepAlive, Sse},
    routing::{get, post},
    Json, Router,
};
use futures::Stream;
use serde_json::Value;
use tokio_stream::wrappers::BroadcastStream;
use tokio_stream::StreamExt as _;

use super::data::shared::resolve_tf;
use super::types::{BarStreamEvent, IndicatorConfig, StreamRequest};
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
    params(
        ("symbol" = String, Path, description = "Symbol to stream e.g. BTCUSDT")
    ),
    request_body = StreamRequest,
    responses(
        (status = 200, description = "SSE bar+indicator stream (text/event-stream, event: bar)")
    ),
    tag = "stream"
)]
pub async fn stream_bars(
    State(state): State<HttpState>,
    Path(symbol): Path<String>,
    Json(req): Json<StreamRequest>,
) -> Sse<impl Stream<Item = Result<Event, Infallible>>> {
    let tf = resolve_tf(&req.tf, state.tf);
    let sym = symbol.to_uppercase();
    let indicator_cfgs = req.indicators.unwrap_or_default();

    // Acquire handles — pins indicator cells in the ledger for the stream lifetime.
    let mut handles: Vec<IndicatorHandle> = Vec::with_capacity(indicator_cfgs.len());
    let mut label_specs: Vec<(String, IndicatorSpec)> = Vec::with_capacity(indicator_cfgs.len());
    for cfg in &indicator_cfgs {
        if let Ok((spec, label)) = build_spec(cfg) {
            if let Ok(h) = state.ledger.acquire_indicator(&sym, tf, spec.clone()) {
                handles.push(h);
                label_specs.push((label, spec));
            }
        }
    }

    let ledger = Arc::clone(&state.ledger);
    let rx = state.bar_bcast.subscribe();

    let stream = BroadcastStream::new(rx).filter_map(move |res| {
        let _ = &handles; // keep handles alive for stream lifetime
        match res {
            Ok(bar) if bar.symbol.to_uppercase() == sym => {
                let indicators = if label_specs.is_empty() {
                    HashMap::new()
                } else {
                    ledger
                        .with_state(&sym, tf, |s| {
                            let mut map = HashMap::new();
                            for (label, spec) in &label_specs {
                                if let Some(cell) = s.indicators.get(spec) {
                                    let row = cell_last_fields(cell);
                                    if !row.is_empty() {
                                        map.insert(label.clone(), row);
                                    }
                                }
                            }
                            map
                        })
                        .unwrap_or_default()
                };
                let event = BarStreamEvent {
                    t: bar.timestamp,
                    o: bar.open,
                    h: bar.high,
                    l: bar.low,
                    c: bar.close,
                    v: bar.volume,
                    indicators,
                };
                let data = serde_json::to_string(&event).unwrap_or_default();
                Some(Ok(Event::default().event("bar").data(data)))
            }
            _ => None,
        }
    });

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

    let stream = BroadcastStream::new(rx).filter_map(|res| match res {
        Ok(batch) => {
            let data = serde_json::to_string(&*batch).unwrap_or_default();
            Some(Ok(Event::default().event("signal").data(data)))
        }
        _ => None,
    });

    Sse::new(stream).keep_alive(KeepAlive::new().interval(Duration::from_secs(15)).text("ping"))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Extract the most-recent row from an indicator cell as a flat field map.
/// Returns an empty map when the cell has no data yet (warm-up period).
fn cell_last_fields(cell: &IndicatorCell) -> HashMap<String, f64> {
    let mut row = HashMap::new();
    for f in cell.field_names() {
        if let Some(col) = cell.column(f) {
            if let Some(Some(v)) = col.back() {
                row.insert((*f).to_string(), *v);
            }
        }
    }
    row
}

fn build_spec(cfg: &IndicatorConfig) -> Result<(IndicatorSpec, String), String> {
    let config_value = Value::Object(cfg.config.clone());
    let spec = IndicatorSpec::from_config(config_value, None)
        .map_err(|e| format!("invalid indicator config: {e}"))?;
    let label = cfg.label.clone().unwrap_or_else(|| spec.canonical_key());
    Ok((spec, label))
}
