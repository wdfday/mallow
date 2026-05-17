//! `GET /api/symbols` — symbols tracked by the ledger, with optional live indicator state.
//! `GET /api/indicators` — static indicator catalogue.
//!
//! ```text
//! GET /api/symbols                  → [{symbol, tf, bars}, ...]
//! GET /api/symbols?indicators=true  → [{symbol, tf, bars, indicators:[...]}, ...]
//! GET /api/indicators               → [{name, params, ...}, ...]
//! ```

use std::collections::BTreeMap;

use axum::{extract::{Query, State}, response::Response, routing::get, Router};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use alm_strategy::catalog;

use super::types::ok;
use super::HttpState;

/// Split a prefixed ledger key like `"binance:BTCUSDT"` into `(exchange, raw_symbol)`.
/// Returns `("", key)` when there is no prefix.
fn split_prefix(key: &str) -> (&str, &str) {
    match key.find(':') {
        Some(pos) => (&key[..pos], &key[pos + 1..]),
        None      => ("", key),
    }
}

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/v1/symbols", get(list_symbols))
        .route("/api/v1/indicators", get(list_indicators_catalog))
}

#[derive(Debug, Deserialize)]
pub struct SymbolsQuery {
    /// Include live indicator cells in the response.
    #[serde(default)]
    pub indicators: bool,
}

#[derive(Debug, Serialize)]
pub struct SymbolInfo {
    pub symbol: String,
    pub tf: String,
    /// Number of bars currently in the ledger ring window.
    pub bars: usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub indicators: Option<Vec<LiveIndicator>>,
}

#[derive(Debug, Serialize)]
pub struct LiveIndicator {
    pub canonical_key: String,
    #[serde(rename = "type")]
    pub type_name: String,
    pub fields: Vec<String>,
    pub config: Value,
    pub refcount: usize,
    pub pinned: bool,
    pub ready_since_t: Option<i64>,
}

#[utoipa::path(
    get,
    path = "/api/v1/symbols",
    params(
        ("indicators" = Option<bool>, Query, description = "Include live indicator cells in the response")
    ),
    responses(
        (status = 200, description = "List of tracked symbols")
    ),
    tag = "live"
)]
pub async fn list_symbols(
    State(state): State<HttpState>,
    Query(q): Query<SymbolsQuery>,
) -> Response {
    // Build flat list first, then group by exchange. Ledger stores keys as
    // `"binance:BTCUSDT"` / `"okx:BTC-USDT"` — we split the prefix off so the
    // wire format groups by exchange and `symbol` is the raw ticker only.
    let mut grouped: BTreeMap<String, Vec<SymbolInfo>> = BTreeMap::new();

    for (key, tf) in state.ledger.keys().into_iter() {
        let (exchange, raw_symbol) = split_prefix(&key);
        let exchange = if exchange.is_empty() { "unknown" } else { exchange }.to_string();

        if let Some(info) = state.ledger.with_state(&key, tf, |s| {
            let bars = s.bar_window.len();
            let indicators = if q.indicators {
                Some(
                    s.indicators
                        .iter()
                        .map(|(spec, cell)| LiveIndicator {
                            canonical_key: spec.canonical_key(),
                            type_name: spec.name.clone(),
                            fields: cell.field_names().iter().map(|f| f.to_string()).collect(),
                            config: spec.config.clone(),
                            refcount: cell.refcount,
                            pinned: cell.pinned,
                            ready_since_t: cell.ready_since_t,
                        })
                        .collect(),
                )
            } else {
                None
            };
            SymbolInfo {
                symbol: raw_symbol.to_string(),
                tf: tf.to_string(),
                bars,
                indicators,
            }
        }) {
            grouped.entry(exchange).or_default().push(info);
        }
    }

    for list in grouped.values_mut() {
        list.sort_by(|a, b| a.symbol.cmp(&b.symbol).then_with(|| a.tf.cmp(&b.tf)));
    }

    ok(grouped)
}

// ── GET /api/indicators ───────────────────────────────────────────────────────

#[utoipa::path(
    get,
    path = "/api/v1/indicators",
    responses(
        (status = 200, description = "Indicator catalogue (static)")
    ),
    tag = "live"
)]
pub async fn list_indicators_catalog() -> Response {
    ok(catalog::all())
}
