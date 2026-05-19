//! Watch handler functions — CRUD + ledger warm-set management.

use alm_ledger::IndicatorSpec;
use alm_strategy::factory::indicator_deps;
use axum::{
    extract::{Path, State},
    http::{HeaderMap, StatusCode},
    response::Response,
    Json,
};
use tracing::{debug, info, warn};

use super::types::{CreateWatchReq, UpdateWatchReq, WatchEntry, WatchSlot, parse_tf};
use crate::http::types::{ok, created, no_content, err};
use crate::http::HttpState;

// ── Error helpers ─────────────────────────────────────────────────────────────

fn not_found() -> Response {
    err(StatusCode::NOT_FOUND, "not found")
}

fn bad_req(msg: &str) -> Response {
    err(StatusCode::BAD_REQUEST, msg.to_owned())
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
    let mut items: Vec<WatchEntry> = store.values().map(|s| s.entry.clone()).collect();
    items.sort_by_key(|e| e.created_at);
    ok(items)
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
    headers: HeaderMap,
    Json(req): Json<CreateWatchReq>,
) -> Response {
    if req.symbols.is_empty() {
        return bad_req("symbols must not be empty");
    }
    if req.webhook_url.is_none() && req.nats_subject.is_none() {
        return bad_req("at least one of webhook_url or nats_subject is required");
    }

    let user_id = req.user_id.or_else(|| {
        headers.get("x-user-id").and_then(|v| v.to_str().ok()).map(|s| s.to_owned())
    });

    let tf = req
        .timeframe
        .as_deref()
        .and_then(parse_tf)
        .unwrap_or(state.tf);

    let (strategy_key, params) = req.spec.to_factory_args();
    let deps = indicator_deps(&strategy_key, &params);

    let mut handles = Vec::new();
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
                Err(e) => warn!(symbol=%sym, spec=?spec, err=%e, "watch: failed to acquire indicator handle"),
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
        user_id,
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
    created(entry)
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
        Some(s) => ok(s.entry.clone()),
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

    if let Some(url) = req.webhook_url  { slot.entry.webhook_url  = Some(url); }
    if let Some(sub) = req.nats_subject { slot.entry.nats_subject = Some(sub); }

    if needs_repin {
        if let Some(syms) = req.symbols  { slot.entry.symbols    = syms; }
        if let Some(spec) = req.spec     { slot.entry.spec        = spec; }
        if let Some(tf)   = req.timeframe { slot.entry.timeframe  = Some(tf); }

        let tf = slot.entry.timeframe.as_deref().and_then(parse_tf).unwrap_or(state.tf);
        let (strategy_key, params) = slot.entry.spec.to_factory_args();
        let deps = indicator_deps(&strategy_key, &params);

        let mut new_handles = Vec::new();
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

        slot._handles = new_handles;
        slot.entry.pinned_indicators = slot._handles.len();
    }

    let entry = slot.entry.clone();
    drop(store);

    if let Err(e) = state.store.update_watch_entry(&entry).await {
        warn!(id=%entry.id, err=%e, "watch update: failed to persist");
    }
    info!(id = %entry.id, symbols = entry.symbols.len(), indicators = entry.pinned_indicators, "watch updated");
    ok(entry)
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
    match state.watches.write().await.remove(&id) {
        Some(_) => {
            state.watch_evaluator.remove_watch(&id);
            if let Err(e) = state.store.delete_watch_entry(&id).await {
                warn!(%id, err=%e, "watch: failed to delete persisted entry");
            }
            info!(%id, "watch deleted");
            no_content()
        }
        None => not_found(),
    }
}

// ── Startup restore ───────────────────────────────────────────────────────────

/// Re-hydrate watch entries from the store on startup.
/// Re-acquires indicator handles so the ledger warm-set is restored.
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

        let mut handles = Vec::new();
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

    tracing::info!(count = store.len(), "watch: restored entries from store");
}
