//! Indicator endpoints.
//!
//! Two orthogonal views are exposed:
//!
//! - `GET /api/indicators` — **static catalogue**: every indicator type the
//!   system can build, with parameter schemas and CEL aliases. Backed by
//!   [`alm_strategy::catalog`] so the live and backtest surfaces share one
//!   source of truth.
//! - `GET /api/indicators/live` — **runtime registry**: every indicator cell
//!   currently materialised in the ledger, with refcount, pinned flag, and
//!   `ready_since_t`. Used by ops / debugging dashboards.

use axum::{extract::State, Json};
use alm_strategy::catalog::{self, IndicatorMeta};
use serde::Serialize;
use serde_json::Value;

use super::HttpState;

/// `GET /api/indicators` — static catalogue (stable across requests).
pub async fn list_indicators_catalog() -> Json<Vec<IndicatorMeta>> {
    Json(catalog::all())
}

#[derive(Debug, Serialize)]
pub struct LiveIndicator {
    pub symbol: String,
    pub tf: String,
    pub canonical_key: String,
    #[serde(rename = "type")]
    pub type_name: String,
    pub fields: Vec<String>,
    pub config: Value,
    pub refcount: usize,
    pub pinned: bool,
    pub ready_since_t: Option<i64>,
}

/// `GET /api/indicators/live` — runtime ledger registry.
pub async fn list_indicators_live(State(state): State<HttpState>) -> Json<Vec<LiveIndicator>> {
    let mut out = Vec::new();
    for (symbol, tf) in state.ledger.keys() {
        let items = state.ledger.with_state(&symbol, tf, |s| {
            s.indicators
                .iter()
                .map(|(spec, cell)| LiveIndicator {
                    symbol: symbol.clone(),
                    tf: tf.to_string(),
                    canonical_key: spec.canonical_key(),
                    type_name: spec.name.clone(),
                    fields: cell.field_names().iter().map(|s| s.to_string()).collect(),
                    config: spec.config.clone(),
                    refcount: cell.refcount,
                    pinned: cell.pinned,
                    ready_since_t: cell.ready_since_t,
                })
                .collect::<Vec<_>>()
        });
        if let Some(items) = items {
            out.extend(items);
        }
    }
    out.sort_by(|a, b| {
        a.symbol.cmp(&b.symbol)
            .then_with(|| a.tf.cmp(&b.tf))
            .then_with(|| a.canonical_key.cmp(&b.canonical_key))
    });
    Json(out)
}
