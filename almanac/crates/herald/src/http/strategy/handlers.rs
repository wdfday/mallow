//! Store handler functions — strategy CRUD handlers.

use axum::{
    extract::{Path, State},
    http::{HeaderMap, StatusCode},
    response::Response,
    Json,
};
use super::super::types::{ok, created, no_content, err, err_code};

use super::types::*;
use super::super::HttpState;

// ── Error helpers ─────────────────────────────────────────────────────────────

pub fn not_found() -> Response {
    err(StatusCode::NOT_FOUND, "not found")
}

pub fn bad_req(msg: &str) -> Response {
    err(StatusCode::BAD_REQUEST, msg.to_owned())
}

pub fn conflict(msg: &str) -> Response {
    err_code(StatusCode::CONFLICT, "CONFLICT", msg.to_owned())
}

pub fn server_err(e: anyhow::Error) -> Response {
    tracing::error!(error = %e, "store error");
    err(StatusCode::INTERNAL_SERVER_ERROR, "internal error")
}

// ── Strategy CRUD ─────────────────────────────────────────────────────────────

#[utoipa::path(
    get,
    path = "/api/v1/strategy/strategies",
    responses((status = 200, description = "List of saved strategies")),
    tag = "strategy"
)]
pub async fn list_strategies(State(state): State<HttpState>) -> Response {
    match state.store.list_strategies().await {
        Ok(items) => ok(items),
        Err(e)    => server_err(e),
    }
}

#[utoipa::path(
    post,
    path = "/api/v1/strategy/strategies",
    responses(
        (status = 201, description = "Strategy created"),
        (status = 409, description = "Conflict — version already exists")
    ),
    tag = "strategy"
)]
pub async fn create_strategy(
    State(state): State<HttpState>,
    headers: HeaderMap,
    Json(req): Json<CreateStrategyReq>,
) -> Response {
    // Validate the script before saving.
    let (errors, _) = alm_strategy::script_lint(&req.spec.script, None);
    if !errors.is_empty() {
        return bad_req(&format!("Script error at line {}: {}", errors[0].line, errors[0].message));
    }

    let user_id = req.user_id.or_else(|| {
        headers.get("x-user-id").and_then(|v| v.to_str().ok()).map(|s| s.to_owned())
    });
    match state.store.create_strategy(req.name.clone(), req.version, req.previous_id, req.label, req.spec, req.notes, user_id).await {
        Ok(s)  => {
            tracing::info!(id = %s.id, name = %s.name, version = s.version, "strategy saved");
            created(s)
        }
        Err(e) if {
            let s = e.to_string();
            s.contains("already exists") || s.contains("duplicate key") || s.contains("unique constraint")
        } => conflict("a strategy with this name/version or script already exists"),
        Err(e) => server_err(e),
    }
}

#[utoipa::path(
    get,
    path = "/api/v1/strategy/strategies/{id}",
    params(("id" = String, Path, description = "Strategy UUID")),
    responses(
        (status = 200, description = "Strategy record"),
        (status = 404, description = "Not found")
    ),
    tag = "strategy"
)]
pub async fn get_strategy(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    match state.store.get_strategy(&id).await {
        Ok(Some(s)) => ok(s),
        Ok(None)    => not_found(),
        Err(e)      => server_err(e),
    }
}


#[utoipa::path(
    put,
    path = "/api/v1/strategy/strategies/{id}",
    params(("id" = String, Path, description = "Strategy UUID")),
    responses(
        (status = 200, description = "Updated strategy record"),
        (status = 404, description = "Not found")
    ),
    tag = "strategy"
)]
pub async fn update_strategy(
    State(state): State<HttpState>,
    Path(id): Path<String>,
    Json(req): Json<UpdateStrategyReq>,
) -> Response {
    match state.store.update_strategy(&id, req).await {
        Ok(Some(s)) => ok(s),
        Ok(None)    => not_found(),
        Err(e)      => server_err(e),
    }
}

#[utoipa::path(
    delete,
    path = "/api/v1/strategy/strategies/{id}",
    params(("id" = String, Path, description = "Strategy UUID")),
    responses(
        (status = 204, description = "Deleted"),
        (status = 404, description = "Not found")
    ),
    tag = "strategy"
)]
pub async fn delete_strategy(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    match state.store.delete_strategy(&id).await {
        Ok(true)  => no_content(),
        Ok(false) => not_found(),
        Err(e)    => server_err(e),
    }
}

#[utoipa::path(
    get,
    path = "/api/v1/strategy/my",
    responses(
        (status = 200, description = "All versions of each strategy owned by the caller, grouped by name (newest first per group)"),
        (status = 401, description = "Missing X-User-ID header")
    ),
    tag = "strategy"
)]
pub async fn list_my_strategies(
    State(state): State<HttpState>,
    headers: HeaderMap,
) -> Response {
    let Some(user_id) = headers.get("x-user-id").and_then(|v| v.to_str().ok()) else {
        return bad_req("missing X-User-ID header");
    };
    match state.store.list_my_chains(user_id).await {
        Ok(chains) => ok(chains),
        Err(e)     => server_err(e),
    }
}

// Suppress dead_code warnings for helper functions used only via trait impls.
#[allow(dead_code)]
fn _unused(_: &str) -> Response { bad_req("") }
