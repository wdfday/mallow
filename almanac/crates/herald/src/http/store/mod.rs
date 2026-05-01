//! Store module — CRUD handlers + domain types + backend dispatch.
//!
//! # Routes (registered in `http/mod.rs`)
//!
//! ```text
//! GET    /api/store/strategies                    list all versions
//! POST   /api/store/strategies                    create (or new version)
//! GET    /api/store/strategies/:id                get by UUID
//! GET    /api/store/strategies/:name/versions     all versions of a name
//! PUT    /api/store/strategies/:id                update label / notes
//! DELETE /api/store/strategies/:id                delete version
//!
//! GET    /api/store/cases                         list all
//! POST   /api/store/cases                         create
//! GET    /api/store/cases/:id                     get one
//! PUT    /api/store/cases/:id                     update fields
//! DELETE /api/store/cases/:id                     delete
//! POST   /api/store/cases/:id/run                 resolve + run → save result
//!
//! GET    /api/store/cases/:id/results             list results for case
//! GET    /api/store/results/:id                   get one result
//! DELETE /api/store/results/:id                   delete result
//! ```

pub mod backend;
pub mod migrate;
pub mod types;

pub use backend::StoreBackend;
pub use types::*;

use std::sync::Arc;

use axum::{
    extract::{Path, State},
    http::StatusCode,
    response::{IntoResponse, Response},
    routing::{get, post},
    Json, Router,
};

use alm_core::signal::Direction;
use alm_data::BarFeed;
use alm_engine::{
    backtest,
    data::{find_parquet_files, load_bars},
    types::BacktestRequest,
};
use alm_strategy::factory::build_strategy;

use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        // ── strategies ──────────────────────────────────────────────────────
        .route("/api/store/strategies",
            get(list_strategies).post(create_strategy))
        .route("/api/store/strategies/:id",
            get(get_strategy).put(update_strategy).delete(delete_strategy))
        .route("/api/store/strategies/:name/versions",
            get(list_strategy_versions))
        // ── cases ───────────────────────────────────────────────────────────
        .route("/api/store/cases",
            get(list_cases).post(create_case))
        .route("/api/store/cases/:id",
            get(get_case).put(update_case).delete(delete_case))
        .route("/api/store/cases/:id/run",     post(run_case))
        .route("/api/store/cases/:id/signals", post(run_case_signals))
        .route("/api/store/cases/:id/results", get(list_results))
        // ── results ─────────────────────────────────────────────────────────
        .route("/api/store/results/:id",
            get(get_result).delete(delete_result))
}

// ── Error helpers ─────────────────────────────────────────────────────────────

fn not_found() -> Response {
    (StatusCode::NOT_FOUND, Json(serde_json::json!({"error": "not found"}))).into_response()
}

fn bad_req(msg: &str) -> Response {
    (StatusCode::BAD_REQUEST, Json(serde_json::json!({"error": msg}))).into_response()
}

fn conflict(msg: &str) -> Response {
    (StatusCode::CONFLICT, Json(serde_json::json!({"error": msg}))).into_response()
}

fn server_err(e: anyhow::Error) -> Response {
    tracing::error!(error = %e, "store error");
    (StatusCode::INTERNAL_SERVER_ERROR, Json(serde_json::json!({"error": "internal error"}))).into_response()
}

// ── Strategy CRUD ─────────────────────────────────────────────────────────────

pub async fn list_strategies(State(state): State<HttpState>) -> Response {
    match state.store.list_strategies().await {
        Ok(items) => Json(items).into_response(),
        Err(e)    => server_err(e),
    }
}

pub async fn create_strategy(
    State(state): State<HttpState>,
    Json(req): Json<CreateStrategyReq>,
) -> Response {
    match state.store.create_strategy(req.name, req.version, req.label, req.spec, req.notes).await {
        Ok(s)  => {
            tracing::info!(id = %s.id, name = %s.name, version = s.version, kind = %s.spec.kind_str(), "strategy saved");
            (StatusCode::CREATED, Json(s)).into_response()
        }
        Err(e) if e.to_string().contains("already exists") => conflict(&e.to_string()),
        Err(e) => server_err(e),
    }
}

pub async fn get_strategy(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    match state.store.get_strategy(&id).await {
        Ok(Some(s)) => Json(s).into_response(),
        Ok(None)    => not_found(),
        Err(e)      => server_err(e),
    }
}

pub async fn list_strategy_versions(
    State(state): State<HttpState>,
    Path(name): Path<String>,
) -> Response {
    match state.store.list_strategy_versions(&name).await {
        Ok(items) => Json(items).into_response(),
        Err(e)    => server_err(e),
    }
}

pub async fn update_strategy(
    State(state): State<HttpState>,
    Path(id): Path<String>,
    Json(req): Json<UpdateStrategyReq>,
) -> Response {
    match state.store.update_strategy(&id, req).await {
        Ok(Some(s)) => Json(s).into_response(),
        Ok(None)    => not_found(),
        Err(e)      => server_err(e),
    }
}

pub async fn delete_strategy(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    match state.store.delete_strategy(&id).await {
        Ok(true)  => StatusCode::NO_CONTENT.into_response(),
        Ok(false) => not_found(),
        Err(e) if e.to_string().contains("referenced") => conflict(&e.to_string()),
        Err(e)    => server_err(e),
    }
}

// ── BacktestCase CRUD ─────────────────────────────────────────────────────────

pub async fn list_cases(State(state): State<HttpState>) -> Response {
    match state.store.list_cases().await {
        Ok(items) => Json(items).into_response(),
        Err(e)    => server_err(e),
    }
}

pub async fn create_case(
    State(state): State<HttpState>,
    Json(req): Json<CreateCaseReq>,
) -> Response {
    match state.store.strategy_exists(&req.strategy_id).await {
        Ok(true)  => {}
        Ok(false) => return bad_req("strategy_id not found"),
        Err(e)    => return server_err(e),
    }
    let capital   = req.capital.unwrap_or_default();
    let execution = req.execution.unwrap_or_default();
    match state.store.create_case(
        req.strategy_id, req.label, req.symbol, req.timeframe,
        req.from_ms, req.to_ms, req.data_source, capital, execution, req.exit,
    ).await {
        Ok(c)  => {
            tracing::info!(id = %c.id, symbol = %c.symbol, strategy_id = %c.strategy_id, "case created");
            (StatusCode::CREATED, Json(c)).into_response()
        }
        Err(e) => server_err(e),
    }
}

pub async fn get_case(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    match state.store.get_case(&id).await {
        Ok(Some(c)) => Json(c).into_response(),
        Ok(None)    => not_found(),
        Err(e)      => server_err(e),
    }
}

pub async fn update_case(
    State(state): State<HttpState>,
    Path(id): Path<String>,
    Json(req): Json<UpdateCaseReq>,
) -> Response {
    if let Some(ref sid) = req.strategy_id {
        match state.store.strategy_exists(sid).await {
            Ok(true)  => {}
            Ok(false) => return bad_req("strategy_id not found"),
            Err(e)    => return server_err(e),
        }
    }
    match state.store.update_case(&id, req).await {
        Ok(Some(c)) => Json(c).into_response(),
        Ok(None)    => not_found(),
        Err(e)      => server_err(e),
    }
}

pub async fn delete_case(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    match state.store.delete_case(&id).await {
        Ok(true)  => StatusCode::NO_CONTENT.into_response(),
        Ok(false) => not_found(),
        Err(e)    => server_err(e),
    }
}

// ── Run a saved case ──────────────────────────────────────────────────────────

pub async fn run_case(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    let (case, strategy) = match state.store.resolve_case(&id).await {
        Ok(Some(pair)) => pair,
        Ok(None)       => return not_found(),
        Err(e) if e.to_string().contains("strategy") => return bad_req(&e.to_string()),
        Err(e)         => return server_err(e),
    };

    let Some(_permit) = Arc::clone(&state.backtest_semaphore).try_acquire_owned().ok() else {
        return (
            StatusCode::TOO_MANY_REQUESTS,
            Json(serde_json::json!({"error": "too many concurrent backtests — retry shortly"})),
        ).into_response();
    };

    let (strategy_key, params) = strategy.spec.to_factory_args();
    let req = BacktestRequest {
        strategy: strategy_key,
        symbol:   case.symbol.clone(),
        params:   if params.is_null() { None } else { Some(params) },
        exit:     case.exit.clone(),
        from:     case.from_ms.and_then(ms_to_date),
        to:       case.to_ms.and_then(ms_to_date),
        initial_capital:        case.capital.initial,
        commission_pct:         case.execution.commission_pct,
        slippage_pct:           case.execution.slippage_pct,
        risk_free_annual:       case.execution.risk_free_annual,
        position_size_pct:      case.capital.position_pct,
        position_size_usd:      case.capital.position_usd,
        position_size_quantity: None,
        max_positions:          case.capital.max_positions,
        market_hours_only:      None,
        data_source:            case.data_source.clone(),
        asset_type:             None,
        timeframe:              case.timeframe.clone(),
        min_strength:           None,
        monte_carlo:            None,
        walk_forward:           None,
    };

    tracing::info!(id = %id, symbol = %case.symbol, strategy_id = %case.strategy_id, "running backtest case");
    let ran_at  = chrono::Utc::now().timestamp_millis();
    let data_dir = Arc::clone(&state.data_dir);

    // Run on blocking thread — same pattern as dispatch().
    let report = match tokio::task::spawn_blocking(move || backtest::run(req, &data_dir)).await {
        Ok(Ok(resp)) => resp,
        Ok(Err(e)) => {
            tracing::warn!(error = %e, "backtest failed");
            return (
                StatusCode::UNPROCESSABLE_ENTITY,
                Json(serde_json::json!({"error": e.to_string()})),
            ).into_response();
        }
        Err(e) => {
            tracing::error!(error = %e, "backtest task panicked");
            return server_err(anyhow::anyhow!("backtest task panicked"));
        }
    };

    // TODO: upload full report JSON to S3 / R2, store key in s3_key.
    let save = state.store.save_result(
        case.id.clone(),
        ran_at,
        None, // s3_key — disabled until S3 client is wired
        report.returns.total_pct,
        report.risk_adjusted.sharpe,
        report.drawdown.max_pct,
        report.trade_stats.win_rate_pct,
        report.trade_stats.total as i64,
    ).await;
    if let Err(e) = save {
        tracing::warn!(error = %e, "failed to persist backtest result row");
    }
    tracing::info!(
        id = %id,
        total_return_pct = report.returns.total_pct,
        sharpe = report.risk_adjusted.sharpe,
        trades = report.trade_stats.total,
        "backtest done"
    );
    Json(report).into_response()
}

// ── Signal replay ─────────────────────────────────────────────────────────────

/// Replay historical bars through the case's strategy and return the raw
/// signal stream — no SimBroker, no position tracking, no report.
///
/// Useful for auditing what a Rhai/CEL strategy would have signalled on
/// historical data, including inline `tp`/`sl` from Rhai scripts.
pub async fn run_case_signals(
    State(state): State<HttpState>,
    Path(id): Path<String>,
) -> Response {
    let (case, strategy) = match state.store.resolve_case(&id).await {
        Ok(Some(pair)) => pair,
        Ok(None)       => return not_found(),
        Err(e) if e.to_string().contains("strategy") => return bad_req(&e.to_string()),
        Err(e)         => return server_err(e),
    };

    let Some(_permit) = Arc::clone(&state.backtest_semaphore).try_acquire_owned().ok() else {
        return (
            StatusCode::TOO_MANY_REQUESTS,
            Json(serde_json::json!({"error": "too many concurrent backtests — retry shortly"})),
        ).into_response();
    };

    let (strategy_key, params) = strategy.spec.to_factory_args();
    let data_dir = Arc::clone(&state.data_dir);

    let signals = match tokio::task::spawn_blocking(move || {
        signal_replay(&data_dir, &case, &strategy_key, &params)
    }).await {
        Ok(Ok(sigs))  => sigs,
        Ok(Err(e)) => {
            tracing::warn!(error = %e, "signal replay failed");
            return (
                StatusCode::UNPROCESSABLE_ENTITY,
                Json(serde_json::json!({"error": e.to_string()})),
            ).into_response();
        }
        Err(e) => return server_err(anyhow::anyhow!("replay task panicked: {e}")),
    };

    tracing::info!(id = %id, signals = signals.len(), "signal replay done");
    Json(signals).into_response()
}

fn signal_replay(
    data_dir: &std::path::Path,
    case: &BacktestCase,
    strategy_key: &str,
    params: &serde_json::Value,
) -> anyhow::Result<Vec<SignalPoint>> {
    let files = find_parquet_files(
        data_dir,
        &case.symbol,
        case.timeframe.as_deref(),
        case.data_source.as_deref(),
    );
    if files.is_empty() {
        return Err(anyhow::anyhow!(
            "no parquet files found for {} {:?}",
            case.symbol,
            case.timeframe
        ));
    }

    let mut feed = load_bars(
        &files,
        &case.symbol,
        case.from_ms,
        case.to_ms,
        false,
        "",
    )?;

    // Rhai: params has no `_live` flag → `from_script()` compile mode
    // (series collection enabled — caller can extend this to return plot data later)
    let mut strat = build_strategy(strategy_key, params)?;
    let mut out   = Vec::new();

    while let Some(bar) = feed.next() {
        for sig in strat.on_bar(&bar) {
            out.push(SignalPoint {
                ts:           sig.timestamp,
                direction:    match sig.direction {
                    Direction::Long  => "long".into(),
                    Direction::Short => "short".into(),
                    Direction::Close => "close".into(),
                },
                price:        sig.price,
                strength:     sig.strength,
                target_price: sig.target_price,
                stop_price:   sig.stop_price,
            });
        }
    }

    Ok(out)
}

// ── BacktestResult handlers ───────────────────────────────────────────────────

pub async fn list_results(State(state): State<HttpState>, Path(case_id): Path<String>) -> Response {
    match state.store.list_results(&case_id).await {
        Ok(items) => Json(items).into_response(),
        Err(e)    => server_err(e),
    }
}

pub async fn get_result(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    match state.store.get_result(&id).await {
        Ok(Some(r)) => Json(r).into_response(),
        Ok(None)    => not_found(),
        Err(e)      => server_err(e),
    }
}

pub async fn delete_result(State(state): State<HttpState>, Path(id): Path<String>) -> Response {
    match state.store.delete_result(&id).await {
        Ok(true)  => StatusCode::NO_CONTENT.into_response(),
        Ok(false) => not_found(),
        Err(e)    => server_err(e),
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Convert Unix milliseconds to `"YYYY-MM-DD"` expected by `BacktestRequest`.
fn ms_to_date(ms: i64) -> Option<String> {
    chrono::DateTime::from_timestamp_millis(ms)
        .map(|dt| dt.format("%Y-%m-%d").to_string())
}
