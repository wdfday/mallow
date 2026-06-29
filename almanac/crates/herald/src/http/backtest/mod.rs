//! Backtest endpoints — thin adapters over [`alm_engine::backtest::run`].
//!
//! The heavy lifting (Parquet loading, engine loop, report aggregation) lives
//! in `alm-engine` as a plain library function. Herald's job here is limited to:
//!
//! - Serialising the request / response envelopes.
//! - Enforcing a concurrency cap via [`HttpState::backtest_semaphore`].
//! - Dispatching the CPU-bound engine run onto `spawn_blocking`.
//!
//! `POST /api/v1/backtest/script` is a **pure run** — it does not save the
//! strategy. Use `POST /api/v1/strategy/strategies` to persist a version.
//!
//! # Concurrency
//!
//! The semaphore is acquired with `try_acquire`; at-capacity requests receive
//! `429 Too Many Requests` instead of queueing. This keeps tail latency
//! bounded — the client can retry with back-off.

use std::sync::Arc;

use axum::{
    extract::State,
    http::StatusCode,
    response::Response,
    Json, Router,
};
use axum::routing::{get, post};
use alm_engine::{
    backtest,
    types::{BacktestRequest, BacktestResponse, ScriptBacktestRequest},
};
use metrics::{gauge, histogram};
use super::types::{ok, err, err_code};
use alm_strategy::catalog::STRATEGY_KEYS;
use alm_strategy::MTF_STRATEGY_KEYS;
use serde::Serialize;
use tracing::{error, info, warn};

use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/v1/strategies", get(list_strategies))
        .route("/api/v1/backtest", post(run_backtest))
        .route("/api/v1/backtest/estimate", post(estimate_backtest))
        .route("/api/v1/backtest/script", post(run_backtest_script))
}

// ── Estimate response ─────────────────────────────────────────────────────────

#[derive(Debug, Serialize)]
pub struct EstimateResponse {
    pub bar_count: usize,
    pub estimated_seconds: f64,
    pub within_limit: bool,
    pub limit: usize,
}

// ── GET /api/strategies ──────────────────────────────────────────────────────

#[derive(Debug, serde::Serialize)]
struct StrategyListResponse {
    named: &'static [&'static str],
    mtf: &'static [&'static str],
}

#[utoipa::path(
    get,
    path = "/api/v1/strategies",
    responses(
        (status = 200, description = "Named and MTF strategy keys")
    ),
    tag = "backtest"
)]
pub async fn list_strategies() -> Response {
    ok(StrategyListResponse { named: STRATEGY_KEYS, mtf: MTF_STRATEGY_KEYS })
}

// ── POST /api/backtest/estimate ──────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/backtest/estimate",
    responses(
        (status = 200, description = "Bar count + estimated run time without running the engine"),
        (status = 400, description = "Bad request")
    ),
    tag = "backtest"
)]
pub async fn estimate_backtest(
    State(state): State<HttpState>,
    Json(req): Json<BacktestRequest>,
) -> Response {
    const MAX_BARS: usize = 100_000;
    let data_dir = Arc::clone(&state.data_dir);
    match tokio::task::spawn_blocking(move || backtest::estimate(&req, &data_dir)).await {
        Ok(Ok((bar_count, estimated_seconds))) => {
            ok(EstimateResponse {
                bar_count,
                estimated_seconds,
                within_limit: bar_count <= MAX_BARS,
                limit: MAX_BARS,
            })
        }
        Ok(Err(e)) => {
            warn!(error = %e, "estimate failed");
            err(StatusCode::BAD_REQUEST, e.to_string())
        }
        Err(e) => {
            error!(error = %e, "estimate spawn_blocking panicked");
            err(StatusCode::INTERNAL_SERVER_ERROR, "internal error")
        }
    }
}

// ── POST /api/backtest ───────────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/backtest",
    responses(
        (status = 200, description = "Backtest report"),
        (status = 400, description = "Bad request"),
        (status = 429, description = "Too many concurrent backtests")
    ),
    tag = "backtest"
)]
pub async fn run_backtest(
    State(state): State<HttpState>,
    Json(req): Json<BacktestRequest>,
) -> Response {
    let Some(_permit) = try_acquire(&state) else {
        metrics::counter!("herald_backtest_requests_total", "type" => "named", "result" => "rejected").increment(1);
        return too_many_requests();
    };
    info!(strategy = %req.strategy, symbol = %req.symbol, "HTTP backtest request");
    let mut req = req;
    inject_weekly_overrides(&state, &mut req);
    match run_blocking(Arc::clone(&state.data_dir), req, "named").await {
        Ok(report) => ok(report),
        Err(r) => r,
    }
}

// ── POST /api/backtest/script ──────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/backtest/script",
    responses(
        (status = 200, description = "Script backtest report"),
        (status = 400, description = "Bad request"),
        (status = 429, description = "Too many concurrent backtests")
    ),
    tag = "backtest"
)]
pub async fn run_backtest_script(
    State(state): State<HttpState>,
    Json(req): Json<ScriptBacktestRequest>,
) -> Response {
    let Some(_permit) = try_acquire(&state) else {
        metrics::counter!("herald_backtest_requests_total", "type" => "script", "result" => "rejected").increment(1);
        return too_many_requests();
    };

    info!(symbol = %req.symbol, "Script backtest request");

    let started_at = std::time::Instant::now();
    let mut base: BacktestRequest = req.into();
    inject_weekly_overrides(&state, &mut base);
    let report = match run_blocking(Arc::clone(&state.data_dir), base, "script").await {
        Ok(r)  => r,
        Err(r) => return r,
    };
    let elapsed_ms = started_at.elapsed().as_millis() as u64;

    info!(
        elapsed_ms,
        total_return = report.returns.total_pct,
        sharpe       = report.risk_adjusted.sharpe,
        trades       = report.trade_stats.total,
        "script backtest done"
    );

    let mut body = serde_json::to_value(&report).unwrap_or_default();
    body["elapsed_ms"] = serde_json::json!(elapsed_ms);
    ok(body)
}

// ── Core helpers ─────────────────────────────────────────────────────────────

struct BacktestPermit(#[allow(dead_code)] tokio::sync::OwnedSemaphorePermit);

impl Drop for BacktestPermit {
    fn drop(&mut self) {
        gauge!("herald_backtest_inflight").decrement(1.0);
    }
}

fn try_acquire(state: &HttpState) -> Option<BacktestPermit> {
    Arc::clone(&state.backtest_semaphore)
        .try_acquire_owned()
        .ok()
        .map(|p| {
            gauge!("herald_backtest_inflight").increment(1.0);
            BacktestPermit(p)
        })
}

/// Run the engine on a blocking thread. Returns `Ok(report)` or `Err(error_response)`.
/// Times out after 300 s — an infinite Rhai loop won't block the server forever.
pub(super) async fn run_blocking(
    data_dir: Arc<std::path::PathBuf>,
    req: BacktestRequest,
    kind: &'static str,
) -> Result<BacktestResponse, Response> {
    const BACKTEST_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(300);
    let t0 = std::time::Instant::now();
    let task = tokio::task::spawn_blocking(move || backtest::run(req, &data_dir));
    let result = tokio::time::timeout(BACKTEST_TIMEOUT, task).await;
    let elapsed_ms = t0.elapsed().as_millis() as f64;
    match result {
        Err(_elapsed) => {
            warn!(elapsed_ms, timeout_secs = 300u64, "backtest timed out");
            metrics::counter!("herald_backtest_timeouts_total").increment(1);
            metrics::counter!("herald_backtest_requests_total", "type" => kind, "result" => "timeout").increment(1);
            Err(err(StatusCode::REQUEST_TIMEOUT, "backtest timed out — reduce date range or try again"))
        }
        Ok(Ok(Ok(resp))) => {
            histogram!("herald_backtest_duration_ms", "type" => kind).record(elapsed_ms);
            metrics::counter!("herald_backtest_requests_total", "type" => kind, "result" => "ok").increment(1);
            Ok(resp)
        }
        Ok(Ok(Err(e))) => {
            warn!(error = %e, "backtest failed");
            metrics::counter!("herald_backtest_requests_total", "type" => kind, "result" => "error").increment(1);
            Err(err(StatusCode::BAD_REQUEST, e.to_string()))
        }
        Ok(Err(e)) => {
            error!(error = %e, "backtest spawn_blocking panicked");
            metrics::counter!("herald_backtest_requests_total", "type" => kind, "result" => "error").increment(1);
            Err(err(StatusCode::INTERNAL_SERVER_ERROR, "internal engine error"))
        }
    }
}

fn too_many_requests() -> Response {
    err_code(
        StatusCode::TOO_MANY_REQUESTS,
        "TOO_MANY_REQUESTS",
        "too many concurrent backtests — retry shortly",
    )
}

fn get_weekly_bars_from_ledger(
    state: &HttpState,
    symbol: &str,
    data_source: Option<&str>,
) -> Option<Vec<alm_core::Bar>> {
    let ledger_key = if symbol.contains(':') {
        symbol.to_string()
    } else {
        let src = data_source
            .map(|s| if s == "bnb" { "binance" } else { s })
            .unwrap_or("binance");
        format!("{}:{}", src, symbol)
    };

    state.ledger.with_state(&ledger_key, alm_core::Timeframe::W1, |s| {
        s.bar_window.iter().cloned().collect::<Vec<_>>()
    })
}

fn inject_weekly_overrides(
    state: &HttpState,
    req: &mut BacktestRequest,
) {
    let mut overrides_needed = Vec::new();
    if req.timeframe.as_deref() == Some("W1") {
        overrides_needed.push("W1".to_string());
    }
    if let Some(ref params) = req.params {
        if let Some(script) = params.get("script").and_then(|v| v.as_str()) {
            let htfs = alm_strategy::probe_script_htfs(script);
            for htf in htfs {
                if htf == alm_core::Timeframe::W1 {
                    overrides_needed.push("W1".to_string());
                }
            }
        }
    }
    if !overrides_needed.is_empty() {
        if let Some(bars) = get_weekly_bars_from_ledger(state, &req.symbol, req.data_source.as_deref()) {
            let mut map = req.history_overrides.take().unwrap_or_default();
            for tf in overrides_needed {
                map.insert(tf, bars.clone());
            }
            req.history_overrides = Some(map);
        }
    }
}


// Workaround: BacktestResponse is only referenced through `Json<>`. Silence
// the dead-import warning in case rustc cannot see through the `Json` layer.
#[allow(dead_code)]
fn _enforce_response_import(_r: BacktestResponse) {}

#[cfg(test)]
mod tests {
    use super::*;
    use alm_core::{Bar, Timeframe};
    use alm_ledger::{Ledger, LedgerConfig};
    use crate::http::StoreBackend;
    use crate::ws_latency::WsLatencyTracker;
    use std::path::PathBuf;

    fn make_test_state() -> HttpState {
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let data_dir = Arc::new(PathBuf::from("/nonexistent-test-dir"));
        let ws_latency = Arc::new(WsLatencyTracker::new());
        let prometheus = metrics_exporter_prometheus::PrometheusBuilder::new()
            .build_recorder()
            .handle();
        let (state, ready) = HttpState::new(
            ledger,
            Timeframe::M1,
            data_dir,
            1,
            StoreBackend::in_memory(),
            ws_latency,
            prometheus,
        );
        ready.store(true, std::sync::atomic::Ordering::Relaxed);
        state
    }

    #[test]
    fn test_inject_weekly_overrides_base_tf() {
        let state = make_test_state();
        // Advance W1 bar for BTCUSDT
        state.ledger.advance(
            Timeframe::W1,
            Bar::new(1719273600000, "binance:BTCUSDT", 60000.0, 61000.0, 59000.0, 60500.0, 100.0)
        ).unwrap();

        let mut req = BacktestRequest {
            strategy: "test".to_string(),
            symbol: "BTCUSDT".to_string(),
            params: None,
            from: None,
            to: None,
            initial_capital: None,
            commission_pct: None,
            slippage_pct: None,
            risk_free_annual: None,
            position_size_pct: None,
            position_size_usd: None,
            position_size_quantity: None,
            max_positions: None,
            strength_sizing: None,
            size_mode: None,
            risk_per_trade_pct: None,
            atr_multiplier: None,
            max_units: None,
            max_position_pct: None,
            pyramid: None,
            data_source: Some("bnb".to_string()),
            asset_type: None,
            timeframe: Some("W1".to_string()),
            monte_carlo: None,
            walk_forward: None,
            intra_bar_mode: None,
            reverse_policy: None,
            history_overrides: None,
        };

        inject_weekly_overrides(&state, &mut req);

        let overrides = req.history_overrides.unwrap();
        assert!(overrides.contains_key("W1"));
        let bars = overrides.get("W1").unwrap();
        assert_eq!(bars.len(), 1);
        assert_eq!(bars[0].close, 60500.0);
    }

    #[test]
    fn test_inject_weekly_overrides_script_mtf() {
        let state = make_test_state();
        // Advance W1 bar for BTCUSDT
        state.ledger.advance(
            Timeframe::W1,
            Bar::new(1719273600000, "binance:BTCUSDT", 60000.0, 61000.0, 59000.0, 60500.0, 100.0)
        ).unwrap();

        let script = r#"
            let w1_ema = ind.ema(20, "W1");
            let m1_ema = ind.ema(10);
        "#;

        let mut req = BacktestRequest {
            strategy: "script".to_string(),
            symbol: "BTCUSDT".to_string(),
            params: Some(serde_json::json!({ "script": script })),
            from: None,
            to: None,
            initial_capital: None,
            commission_pct: None,
            slippage_pct: None,
            risk_free_annual: None,
            position_size_pct: None,
            position_size_usd: None,
            position_size_quantity: None,
            max_positions: None,
            strength_sizing: None,
            size_mode: None,
            risk_per_trade_pct: None,
            atr_multiplier: None,
            max_units: None,
            max_position_pct: None,
            pyramid: None,
            data_source: Some("bnb".to_string()),
            asset_type: None,
            timeframe: Some("M1".to_string()),
            monte_carlo: None,
            walk_forward: None,
            intra_bar_mode: None,
            reverse_policy: None,
            history_overrides: None,
        };

        inject_weekly_overrides(&state, &mut req);

        let overrides = req.history_overrides.unwrap();
        assert!(overrides.contains_key("W1"));
        let bars = overrides.get("W1").unwrap();
        assert_eq!(bars.len(), 1);
        assert_eq!(bars[0].close, 60500.0);
    }
}
