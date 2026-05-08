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

use std::collections::HashMap;
use std::sync::Arc;

use alm_ledger::{IndicatorHandle, IndicatorSpec};
use alm_strategy::factory::indicator_deps;
use utoipa::ToSchema;
use axum::{
    extract::{Path, State},
    http::StatusCode,
    response::{IntoResponse, Response},
    routing::get,
    Json, Router,
};
use serde::{Deserialize, Serialize};
use tokio::sync::RwLock;
use tracing::{debug, info, warn};

use super::store::types::StrategySpec;

// ── Domain types ──────────────────────────────────────────────────────────────

/// A registered watch — runs a strategy on live bars and dispatches
/// signals to a webhook or NATS subject instead of executing trades.
#[derive(Debug, Clone, Serialize, Deserialize, ToSchema)]
pub struct WatchEntry {
    pub id: String,
    pub symbols: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub timeframe: Option<String>,
    #[serde(rename = "strategy_spec")]
    pub spec: StrategySpec,
    /// HTTP endpoint to POST signal JSON to when strategy fires.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub webhook_url: Option<String>,
    /// NATS subject to publish SignalMsg to (e.g. strategist subscribes).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub nats_subject: Option<String>,
    /// Number of indicator handles pinned in the ledger by this entry.
    /// Informational — decrements to 0 when the entry is deleted.
    pub pinned_indicators: usize,
    pub created_at: i64,
}

#[derive(Debug, Deserialize, ToSchema)]
pub struct CreateWatchReq {
    pub symbols: Vec<String>,
    #[serde(default)]
    pub timeframe: Option<String>,
    #[serde(rename = "strategy_spec")]
    pub spec: StrategySpec,
    #[serde(default)]
    pub webhook_url: Option<String>,
    #[serde(default)]
    pub nats_subject: Option<String>,
}

/// Partial update for a watch entry.
///
/// Only provided fields are applied. `symbols` and `strategy_spec` trigger a
/// full handle re-pin (old handles dropped, new ones acquired). `webhook_url`
/// and `nats_subject` are simple metadata — no ledger side-effects.
#[derive(Debug, Deserialize, ToSchema)]
pub struct UpdateWatchReq {
    /// Replace the symbol list. Triggers indicator handle re-pin.
    #[serde(default)]
    pub symbols: Option<Vec<String>>,
    /// Replace the strategy spec. Triggers indicator handle re-pin.
    #[serde(default, rename = "strategy_spec")]
    pub spec: Option<StrategySpec>,
    /// Replace the timeframe. Triggers indicator handle re-pin.
    #[serde(default)]
    pub timeframe: Option<String>,
    /// Update the webhook URL. Pass `null` to clear.
    #[serde(default)]
    pub webhook_url: Option<String>,
    /// Update the NATS subject. Pass `null` to clear.
    #[serde(default)]
    pub nats_subject: Option<String>,
}

use super::HttpState;

// ── WatchSlot ─────────────────────────────────────────────────────────────────

/// Internal storage unit. `entry` is what we expose over HTTP; `_handles`
/// keeps the indicator refcounts alive for the lifetime of the slot.
/// Dropping a `WatchSlot` releases all handles (refcount--) which triggers
/// grace eviction in the ledger if no other consumer holds the same spec.
pub struct WatchSlot {
    pub entry: WatchEntry,
    pub _handles: Vec<IndicatorHandle>,
}

// ── Store ─────────────────────────────────────────────────────────────────────

pub type WatchStore = Arc<RwLock<HashMap<String, WatchSlot>>>;

pub fn new_store() -> WatchStore {
    Arc::new(RwLock::new(HashMap::new()))
}

// ── Routes ────────────────────────────────────────────────────────────────────

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/v1/watch", get(list_watches).post(create_watch))
        .route("/api/v1/watch/:id", get(get_watch).put(update_watch).delete(delete_watch))
}

// ── Error helpers ─────────────────────────────────────────────────────────────

fn not_found() -> Response {
    (StatusCode::NOT_FOUND, Json(serde_json::json!({"error": "not found"}))).into_response()
}

fn bad_req(msg: &str) -> Response {
    (StatusCode::BAD_REQUEST, Json(serde_json::json!({"error": msg}))).into_response()
}

// ── Handlers ──────────────────────────────────────────────────────────────────

#[utoipa::path(
    get,
    path = "/api/v1/watch",
    responses((status = 200, description = "List of watch entries")),
    tag = "watch"
)]
pub async fn list_watches(State(state): State<HttpState>) -> Response {
    let store = state.watches.read().await;
    let mut items: Vec<&WatchEntry> = store.values().map(|s| &s.entry).collect();
    items.sort_by_key(|e| e.created_at);
    Json(items).into_response()
}

#[utoipa::path(
    post,
    path = "/api/v1/watch",
    responses(
        (status = 201, description = "Watch entry created"),
        (status = 400, description = "Validation error")
    ),
    tag = "watch"
)]
pub async fn create_watch(
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

#[utoipa::path(
    get,
    path = "/api/v1/watch/{id}",
    params(("id" = String, Path, description = "Watch entry UUID")),
    responses(
        (status = 200, description = "Watch entry"),
        (status = 404, description = "Not found")
    ),
    tag = "watch"
)]
pub async fn get_watch(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    match state.watches.read().await.get(&id) {
        Some(s) => Json(s.entry.clone()).into_response(),
        None    => not_found(),
    }
}

#[utoipa::path(
    put,
    path = "/api/v1/watch/{id}",
    params(("id" = String, Path, description = "Watch entry UUID")),
    request_body = UpdateWatchReq,
    responses(
        (status = 200, description = "Updated watch entry"),
        (status = 400, description = "Validation error"),
        (status = 404, description = "Not found")
    ),
    tag = "watch"
)]
pub async fn update_watch(
    State(state): State<HttpState>,
    Path(id): Path<String>,
    Json(req): Json<UpdateWatchReq>,
) -> Response {
    // Validate before acquiring the write lock.
    if let Some(ref syms) = req.symbols {
        if syms.is_empty() {
            return bad_req("symbols must not be empty");
        }
    }

    let mut store = state.watches.write().await;
    let Some(slot) = store.get_mut(&id) else {
        return not_found();
    };

    let needs_repin = req.symbols.is_some() || req.spec.is_some() || req.timeframe.is_some();

    // Apply simple metadata fields.
    if let Some(url) = req.webhook_url { slot.entry.webhook_url   = Some(url); }
    if let Some(sub) = req.nats_subject { slot.entry.nats_subject = Some(sub); }

    if needs_repin {
        if let Some(syms) = req.symbols  { slot.entry.symbols    = syms; }
        if let Some(spec) = req.spec     { slot.entry.spec        = spec; }
        if let Some(tf)   = req.timeframe { slot.entry.timeframe  = Some(tf); }

        let tf = slot.entry.timeframe.as_deref().and_then(parse_tf).unwrap_or(state.tf);
        let (strategy_key, params) = slot.entry.spec.to_factory_args();
        let deps = indicator_deps(&strategy_key, &params);

        let mut new_handles: Vec<IndicatorHandle> = Vec::new();
        for sym in &slot.entry.symbols {
            state.ledger.ensure_symbol(sym, tf, None);
            for dep in &deps {
                let spec = match IndicatorSpec::from_config(dep.config.clone(), dep.source_tf) {
                    Ok(s)  => s,
                    Err(e) => {
                        warn!(symbol=%sym, err=%e, "watch update: skipping invalid dep");
                        continue;
                    }
                };
                match state.ledger.acquire_indicator(sym, tf, spec.clone()) {
                    Ok(h) => {
                        debug!(symbol=%sym, spec=%spec.canonical_key(), "watch update: pinned indicator");
                        new_handles.push(h);
                    }
                    Err(e) => warn!(symbol=%sym, spec=?spec, err=%e, "watch update: acquire failed"),
                }
            }
        }

        // Drop old handles (refcount--) only after new ones are acquired.
        slot._handles = new_handles;
        slot.entry.pinned_indicators = slot._handles.len();
    }

    let entry = slot.entry.clone();
    drop(store);

    if let Err(e) = state.store.update_watch_entry(&entry).await {
        warn!(id=%entry.id, err=%e, "watch update: failed to persist");
    }
    info!(
        id = %entry.id,
        symbols = entry.symbols.len(),
        indicators = entry.pinned_indicators,
        "watch updated"
    );
    Json(entry).into_response()
}

#[utoipa::path(
    delete,
    path = "/api/v1/watch/{id}",
    params(("id" = String, Path, description = "Watch entry UUID")),
    responses(
        (status = 204, description = "Deleted"),
        (status = 404, description = "Not found")
    ),
    tag = "watch"
)]
pub async fn delete_watch(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    // WatchSlot drop releases all IndicatorHandles → refcount-- in ledger.
    match state.watches.write().await.remove(&id) {
        Some(_) => {
            // Drop cached strategy handles in the evaluator immediately.
            state.watch_evaluator.remove_watch(&id);
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
