//! Server-Sent Events — real-time push for bars and signals.
//!
//! ```text
//! GET /api/stream/:symbol   → bar events for one symbol  (event: "bar")
//! GET /api/stream/signals   → all signal batches          (event: "signal")
//! ```
//!
//! Both streams send JSON-encoded payloads. Clients use `EventSource` (browser)
//! or any SSE-capable HTTP client. The server sends a keep-alive comment every
//! 15 seconds so proxies don't close idle connections.

use std::convert::Infallible;
use std::time::Duration;

use axum::{
    extract::{Path, State},
    response::sse::{Event, KeepAlive, Sse},
    routing::get,
    Router,
};
use futures::Stream;
use tokio_stream::wrappers::BroadcastStream;
use tokio_stream::StreamExt as _;

use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/stream/signals", get(stream_signals))
        .route("/api/stream/:symbol", get(stream_bars))
}

// ── Bars ──────────────────────────────────────────────────────────────────────

#[utoipa::path(
    get,
    path = "/api/stream/{symbol}",
    params(
        ("symbol" = String, Path, description = "Symbol to stream bars for e.g. BTCUSDT")
    ),
    responses(
        (status = 200, description = "SSE bar stream (text/event-stream, event: bar)")
    ),
    tag = "stream"
)]
pub async fn stream_bars(
    State(state): State<HttpState>,
    Path(symbol): Path<String>,
) -> Sse<impl Stream<Item = Result<Event, Infallible>>> {
    let rx = state.bar_bcast.subscribe();
    let symbol_upper = symbol.to_uppercase();

    let stream = BroadcastStream::new(rx).filter_map(move |res| match res {
        Ok(bar) if bar.symbol.to_uppercase() == symbol_upper => {
            let data = serde_json::to_string(&bar).unwrap_or_default();
            Some(Ok(Event::default().event("bar").data(data)))
        }
        _ => None,
    });

    Sse::new(stream).keep_alive(KeepAlive::new().interval(Duration::from_secs(15)).text("ping"))
}

// ── Signals ───────────────────────────────────────────────────────────────────

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
