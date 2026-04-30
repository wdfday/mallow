//! `GET /api/symbols` — list every symbol currently tracked by the ledger.

use std::collections::BTreeSet;

use axum::{extract::State, routing::get, Json, Router};

use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new().route("/api/symbols", get(list_symbols))
}

/// Return a sorted, deduplicated list of symbols that have at least one
/// (symbol, tf) pair in the ledger. Timeframe is irrelevant to the response —
/// clients deduplicate across timeframes.
pub async fn list_symbols(State(state): State<HttpState>) -> Json<Vec<String>> {
    let mut set: BTreeSet<String> = BTreeSet::new();
    for (sym, _tf) in state.ledger.keys() {
        set.insert(sym);
    }
    Json(set.into_iter().collect())
}
