//! Watchlist — admin-managed warm-set + future live signal dispatch.
//!
//! # Dual purpose
//!
//! 1. **Ledger warm-set**: creating a watch entry pins the indicator
//!    dependencies declared in its strategy spec into the ledger for every
//!    listed symbol.  Deleting the entry drops the `IndicatorHandle`s,
//!    decrementing refcounts and scheduling grace eviction when they reach 0.
//!    This lets admins control which indicators stay warm via HTTP CRUD
//!    instead of code changes.
//!
//! 2. **Signal dispatch** (future): the same entry will drive live bar
//!    evaluation and forward signals to `webhook_url` / `nats_subject`.
//!    Not yet wired to the bar pipeline.
//!
//! # Routes
//!
//! ```text
//! GET    /api/watch            list all watch entries
//! POST   /api/watch            create entry + pin indicators in ledger
//! GET    /api/watch/:id        get one
//! DELETE /api/watch/:id        remove + release indicator handles
//! ```

pub mod types;

use std::collections::HashMap;
use std::sync::Arc;

use alm_ledger::{IndicatorHandle, IndicatorSpec};
use alm_strategy::factory::indicator_deps;
use axum::{
    extract::{Path, State},
    http::StatusCode,
    response::{IntoResponse, Response},
    routing::get,
    Json, Router,
};
use tokio::sync::RwLock;
use tracing::{debug, info, warn};

pub use types::{CreateWatchReq, WatchEntry};

use super::HttpState;

// ── WatchSlot ─────────────────────────────────────────────────────────────────

/// Internal storage unit. `entry` is what we expose over HTTP; `_handles`
/// keeps the indicator refcounts alive for the lifetime of the slot.
/// Dropping a `WatchSlot` releases all handles (refcount--) which triggers
/// grace eviction in the ledger if no other consumer holds the same spec.
pub(crate) struct WatchSlot {
    entry: WatchEntry,
    _handles: Vec<IndicatorHandle>,
}

// ── Store ─────────────────────────────────────────────────────────────────────

pub type WatchStore = Arc<RwLock<HashMap<String, WatchSlot>>>;

pub fn new_store() -> WatchStore {
    Arc::new(RwLock::new(HashMap::new()))
}

// ── Routes ────────────────────────────────────────────────────────────────────

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/watch", get(list_watches).post(create_watch))
        .route("/api/watch/:id", get(get_watch).delete(delete_watch))
}

// ── Error helpers ─────────────────────────────────────────────────────────────

fn not_found() -> Response {
    (StatusCode::NOT_FOUND, Json(serde_json::json!({"error": "not found"}))).into_response()
}

fn bad_req(msg: &str) -> Response {
    (StatusCode::BAD_REQUEST, Json(serde_json::json!({"error": msg}))).into_response()
}

// ── Handlers ──────────────────────────────────────────────────────────────────

async fn list_watches(State(state): State<HttpState>) -> Response {
    let store = state.watches.read().await;
    let mut items: Vec<&WatchEntry> = store.values().map(|s| &s.entry).collect();
    items.sort_by_key(|e| e.created_at);
    Json(items).into_response()
}

async fn create_watch(
    State(state): State<HttpState>,
    Json(req): Json<CreateWatchReq>,
) -> Response {
    if req.symbols.is_empty() {
        return bad_req("symbols must not be empty");
    }
    if req.webhook_url.is_none() && req.nats_subject.is_none() {
        return bad_req("at least one of webhook_url or nats_subject is required");
    }

    let tf = req
        .timeframe
        .as_deref()
        .and_then(parse_tf)
        .unwrap_or(state.tf);

    // Resolve indicator deps from the strategy spec.
    let (strategy_key, params) = req.spec.to_factory_args();
    let deps = indicator_deps(&strategy_key, &params);

    // Pin indicators in the ledger for every listed symbol.
    let mut handles: Vec<IndicatorHandle> = Vec::new();
    for sym in &req.symbols {
        state.ledger.ensure_symbol(sym, tf, None);
        for dep in &deps {
            let spec = match IndicatorSpec::from_config(dep.config.clone(), dep.source_tf) {
                Ok(s)  => s,
                Err(e) => {
                    warn!(symbol=%sym, config=%dep.config, err=%e, "watch: skipping invalid indicator dep");
                    continue;
                }
            };
            match state.ledger.acquire_indicator(sym, tf, spec.clone()) {
                Ok(h) => {
                    debug!(symbol=%sym, spec=%spec.canonical_key(), "watch: pinned indicator");
                    handles.push(h);
                }
                Err(e) => {
                    warn!(symbol=%sym, spec=?spec, err=%e, "watch: failed to acquire indicator handle");
                }
            }
        }
    }

    let id = uuid::Uuid::now_v7().to_string();
    let entry = WatchEntry {
        id: id.clone(),
        symbols: req.symbols,
        timeframe: req.timeframe,
        spec: req.spec,
        webhook_url: req.webhook_url,
        nats_subject: req.nats_subject,
        pinned_indicators: handles.len(),
        created_at: chrono::Utc::now().timestamp_millis(),
    };

    if let Err(e) = state.store.save_watch_entry(&entry).await {
        warn!(id=%entry.id, err=%e, "watch: failed to persist entry — continuing in-memory");
    }

    let slot = WatchSlot { entry: entry.clone(), _handles: handles };
    state.watches.write().await.insert(id, slot);
    info!(
        id = %entry.id,
        symbols = entry.symbols.len(),
        strategy = %entry.spec.kind_str(),
        indicators = entry.pinned_indicators,
        "watch created"
    );
    (StatusCode::CREATED, Json(entry)).into_response()
}

async fn get_watch(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    match state.watches.read().await.get(&id) {
        Some(s) => Json(s.entry.clone()).into_response(),
        None    => not_found(),
    }
}

async fn delete_watch(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    // WatchSlot drop releases all IndicatorHandles → refcount-- in ledger.
    match state.watches.write().await.remove(&id) {
        Some(_) => {
            if let Err(e) = state.store.delete_watch_entry(&id).await {
                warn!(%id, err=%e, "watch: failed to delete persisted entry");
            }
            info!(%id, "watch deleted");
            StatusCode::NO_CONTENT.into_response()
        }
        None => not_found(),
    }
}

// ── Startup restore ───────────────────────────────────────────────────────────

/// Re-hydrate watch entries from the store on startup.
/// Re-acquires indicator handles so the ledger warm-set is restored.
/// Called once from `main.rs` after `HttpState` is created.
pub async fn restore_from_store(state: &HttpState) {
    let entries = match state.store.list_watch_entries().await {
        Ok(e)  => e,
        Err(e) => {
            warn!(err=%e, "watch: failed to load persisted entries — starting empty");
            return;
        }
    };

    if entries.is_empty() {
        return;
    }

    let mut store = state.watches.write().await;
    for mut entry in entries {
        let tf = entry.timeframe.as_deref().and_then(parse_tf).unwrap_or(state.tf);
        let (strategy_key, params) = entry.spec.to_factory_args();
        let deps = indicator_deps(&strategy_key, &params);

        let mut handles: Vec<IndicatorHandle> = Vec::new();
        for sym in &entry.symbols {
            state.ledger.ensure_symbol(sym, tf, None);
            for dep in &deps {
                let spec = match IndicatorSpec::from_config(dep.config.clone(), dep.source_tf) {
                    Ok(s)  => s,
                    Err(e) => {
                        warn!(symbol=%sym, err=%e, "watch restore: skipping invalid dep");
                        continue;
                    }
                };
                match state.ledger.acquire_indicator(sym, tf, spec) {
                    Ok(h)  => handles.push(h),
                    Err(e) => warn!(symbol=%sym, err=%e, "watch restore: acquire failed"),
                }
            }
        }

        entry.pinned_indicators = handles.len();
        let id = entry.id.clone();
        store.insert(id, WatchSlot { entry, _handles: handles });
    }

    let n = store.len();
    tracing::info!(count=n, "watch: restored entries from store");
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn parse_tf(s: &str) -> Option<alm_core::Timeframe> {
    match s.to_ascii_uppercase().as_str() {
        "M1"  => Some(alm_core::Timeframe::M1),
        "M5"  => Some(alm_core::Timeframe::M5),
        "M15" => Some(alm_core::Timeframe::M15),
        "M30" => Some(alm_core::Timeframe::M30),
        "H1"  => Some(alm_core::Timeframe::H1),
        "H4"  => Some(alm_core::Timeframe::H4),
        "D1"  => Some(alm_core::Timeframe::D1),
        "W1"  => Some(alm_core::Timeframe::W1),
        _     => None,
    }
}
