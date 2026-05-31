//! `GET /api/symbols` — symbols tracked by the ledger, with optional live indicator state.
//! `GET /api/indicators` — static indicator catalogue.
//!
//! ```text
//! GET /api/symbols
//!   → { "Binance": [{ symbol, timeframes: [{tf, bars}] }], ... }
//! GET /api/symbols?indicators=true
//!   → { "Binance": [{ symbol, timeframes: [{tf, bars, indicators:[...]}] }], ... }
//! GET /api/indicators
//!   → [{name, params, ...}, ...]
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

fn display_source(src: &str) -> &str {
    match src {
        "binance" => "Binance",
        "okx"     => "OKX",
        "bybit"   => "Bybit",
        other     => other,
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
pub struct TfInfo {
    pub tf: String,
    pub bars: usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub indicators: Option<Vec<LiveIndicator>>,
}

#[derive(Debug, Serialize)]
pub struct SymbolInfo {
    pub symbol: String,
    pub timeframes: Vec<TfInfo>,
}

/// One indicator output field with its semantic type (`f64` scalar vs `bool`
/// 0/1 flag). Mirrors `alm_strategy::catalog::OutputField`.
#[derive(Debug, Serialize)]
pub struct FieldInfo {
    pub name: String,
    /// `"f64"` (scalar) | `"bool"` (0/1 flag).
    #[serde(rename = "type")]
    pub type_: &'static str,
}

#[derive(Debug, Serialize)]
pub struct LiveIndicator {
    pub canonical_key: String,
    #[serde(rename = "type")]
    pub type_name: String,
    pub fields: Vec<FieldInfo>,
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
        (status = 200, description = "List of tracked symbols grouped by exchange")
    ),
    tag = "live"
)]
pub async fn list_symbols(
    State(state): State<HttpState>,
    Query(q): Query<SymbolsQuery>,
) -> Response {
    // Group by exchange → symbol → timeframes. Ledger keys are `"binance:BTCUSDT"`.
    // Result shape: { "Binance": [{ symbol, timeframes: [{tf, bars, indicators?}] }] }
    let mut grouped: BTreeMap<String, BTreeMap<String, Vec<TfInfo>>> = BTreeMap::new();

    for (key, tf) in state.ledger.keys().into_iter() {
        let (exchange, raw_symbol) = split_prefix(&key);
        let exchange = display_source(if exchange.is_empty() { "unknown" } else { exchange }).to_string();

        if let Some(tf_info) = state.ledger.with_state(&key, tf, |s| {
            let bars = s.bar_window.len();
            let indicators = if q.indicators {
                Some(
                    s.indicators
                        .iter()
                        .map(|(spec, cell)| LiveIndicator {
                            canonical_key: spec.canonical_key(),
                            type_name: spec.name.clone(),
                            fields: cell.field_names().iter().map(|f| FieldInfo {
                                name: f.to_string(),
                                type_: alm_indicator::field_kind(f).as_str(),
                            }).collect(),
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
            TfInfo { tf: tf.to_string(), bars, indicators }
        }) {
            grouped
                .entry(exchange)
                .or_default()
                .entry(raw_symbol.to_string())
                .or_default()
                .push(tf_info);
        }
    }

    // Flatten inner BTreeMap into sorted Vec<SymbolInfo> with sorted timeframes.
    let result: BTreeMap<String, Vec<SymbolInfo>> = grouped
        .into_iter()
        .map(|(exchange, symbols)| {
            let mut sym_list: Vec<SymbolInfo> = symbols
                .into_iter()
                .map(|(symbol, mut tfs)| {
                    tfs.sort_by(|a, b| a.tf.cmp(&b.tf));
                    SymbolInfo { symbol, timeframes: tfs }
                })
                .collect();
            sym_list.sort_by(|a, b| a.symbol.cmp(&b.symbol));
            (exchange, sym_list)
        })
        .collect();

    ok(result)
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
