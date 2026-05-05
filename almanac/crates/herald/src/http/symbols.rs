//! `GET /api/symbols` — symbols tracked by the ledger, with optional live indicator state.
//! `GET /api/indicators` — static indicator catalogue.
//!
//! ```text
//! GET /api/symbols                  → [{symbol, tf, bars}, ...]
//! GET /api/symbols?indicators=true  → [{symbol, tf, bars, indicators:[...]}, ...]
//! GET /api/indicators               → [{name, params, ...}, ...]
//! ```

use axum::{extract::{Query, State}, routing::get, Json, Router};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use alm_strategy::catalog::{self, IndicatorMeta};

use super::HttpState;

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
) -> Json<Vec<SymbolInfo>> {
    let mut out: Vec<SymbolInfo> = state
        .ledger
        .keys()
        .into_iter()
        .filter_map(|(symbol, tf)| {
            state.ledger.with_state(&symbol, tf, |s| {
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
                SymbolInfo { symbol: symbol.clone(), tf: tf.to_string(), bars, indicators }
            })
        })
        .collect();

    out.sort_by(|a, b| a.symbol.cmp(&b.symbol).then_with(|| a.tf.cmp(&b.tf)));
    Json(out)
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
pub async fn list_indicators_catalog() -> Json<Vec<IndicatorMeta>> {
    Json(catalog::all())
}
