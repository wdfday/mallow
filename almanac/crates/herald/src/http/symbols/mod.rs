//! `GET /api/symbols` — symbols tracked by the ledger.
//! `GET /api/indicators` — static indicator catalogue.
//!
//! ```text
//! GET /api/symbols
//!   → { "Binance": [{ symbol, timeframes: [{tf, bars}] }], ... }
//! GET /api/indicators
//!   → [{name, params, ...}, ...]
//! ```

use std::collections::BTreeMap;

use axum::{extract::State, response::Response, routing::get, Router};
use serde::Serialize;
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

#[derive(Debug, Serialize)]
pub struct TfInfo {
    pub tf: String,
    pub bars: usize,
}

#[derive(Debug, Serialize)]
pub struct SymbolInfo {
    pub symbol: String,
    pub timeframes: Vec<TfInfo>,
}

#[utoipa::path(
    get,
    path = "/api/v1/symbols",
    responses(
        (status = 200, description = "List of tracked symbols grouped by exchange")
    ),
    tag = "live"
)]
pub async fn list_symbols(
    State(state): State<HttpState>,
) -> Response {
    // Group by exchange → symbol → timeframes. Ledger keys are `"binance:BTCUSDT"`.
    // Result shape: { "Binance": [{ symbol, timeframes: [{tf, bars}] }] }
    let mut grouped: BTreeMap<String, BTreeMap<String, Vec<TfInfo>>> = BTreeMap::new();

    for (key, tf) in state.ledger.keys().into_iter() {
        let (exchange, raw_symbol) = split_prefix(&key);
        let exchange = display_source(if exchange.is_empty() { "unknown" } else { exchange }).to_string();

        if let Some(tf_info) = state.ledger.with_state(&key, tf, |s| {
            let bars = s.bar_window.len();
            TfInfo { tf: tf.to_string(), bars }
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
