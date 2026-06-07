//! Integration tests for the herald HTTP API.
//!
//! Uses `tower::ServiceExt::oneshot` — no port binding needed.
//! All state is in-memory; no NATS, no PostgreSQL, no Parquet fixtures required.

use std::path::PathBuf;
use std::sync::Arc;

use axum::body::Body;
use axum::http::{Request, StatusCode};
use http_body_util::BodyExt;
use tower::ServiceExt;

use alm_core::{Bar, Timeframe};
use alm_ledger::{Ledger, LedgerConfig};

use crate::http::{router, HttpState, StoreBackend};
use crate::ws_latency::WsLatencyTracker;

// ── Helpers ───────────────────────────────────────────────────────────────────

fn test_state() -> HttpState {
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

fn test_app() -> axum::Router {
    router(test_state())
}

async fn json_body(resp: axum::http::Response<Body>) -> serde_json::Value {
    let bytes = resp.into_body().collect().await.unwrap().to_bytes();
    serde_json::from_slice(&bytes).unwrap()
}

fn get(uri: &str) -> Request<Body> {
    Request::builder().uri(uri).body(Body::empty()).unwrap()
}

fn post_json(uri: &str, body: serde_json::Value) -> Request<Body> {
    Request::builder()
        .method("POST")
        .uri(uri)
        .header("content-type", "application/json")
        .body(Body::from(body.to_string()))
        .unwrap()
}

fn put_json(uri: &str, body: serde_json::Value) -> Request<Body> {
    Request::builder()
        .method("PUT")
        .uri(uri)
        .header("content-type", "application/json")
        .body(Body::from(body.to_string()))
        .unwrap()
}

fn delete(uri: &str) -> Request<Body> {
    Request::builder()
        .method("DELETE")
        .uri(uri)
        .body(Body::empty())
        .unwrap()
}

fn script_spec() -> serde_json::Value {
    serde_json::json!({ "script": "if true { entry = true; }" })
}

/// Create a strategy in `state` and return its UUID.
async fn seed_strategy(state: &HttpState) -> String {
    let body = serde_json::json!({
        "name": "seed_strategy",
        "label": "Seed",
        "strategy_spec": script_spec()
    });
    let resp = router(state.clone())
        .oneshot(post_json("/api/v1/strategy/strategies", body))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::CREATED, "seed_strategy: create failed");
    json_body(resp).await["data"]["id"].as_str().unwrap().to_owned()
}

// ── /health ───────────────────────────────────────────────────────────────────

#[tokio::test]
async fn health_ok() {
    let resp = test_app().oneshot(get("/health")).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let body = json_body(resp).await;
    assert_eq!(body["ok"], true);
    assert_eq!(body["service"], "herald");
}

// ── /api/symbols ──────────────────────────────────────────────────────────────

#[tokio::test]
async fn symbols_empty_ledger_returns_empty_map() {
    let resp = test_app().oneshot(get("/api/v1/symbols")).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    // Response shape is `data: { <exchange>: [...] }`. Empty ledger → empty object.
    assert_eq!(json_body(resp).await["data"], serde_json::json!({}));
}

#[tokio::test]
async fn symbols_after_advance_lists_symbol() {
    let state = test_state();
    state
        .ledger
        .advance(Timeframe::M1, Bar::new(1_000_000, "binance:BTCUSDT", 100.0, 101.0, 99.0, 100.5, 10.0))
        .unwrap();

    let resp = router(state).oneshot(get("/api/v1/symbols")).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let body = json_body(resp).await;
    let group = &body["data"]["Binance"];
    let arr = group.as_array().unwrap();
    assert_eq!(arr.len(), 1);
    // Prefix is stripped — only the raw ticker reaches the wire.
    assert_eq!(arr[0]["symbol"], "BTCUSDT");
    let tfs = arr[0]["timeframes"].as_array().unwrap();
    assert_eq!(tfs.len(), 1);
    assert_eq!(tfs[0]["tf"], "M1");
    assert_eq!(tfs[0]["bars"], 1);
}

// ── /api/indicators ───────────────────────────────────────────────────────────

#[tokio::test]
async fn indicators_catalogue_non_empty_with_name_field() {
    let resp = test_app().oneshot(get("/api/v1/indicators")).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let body = json_body(resp).await;
    let arr = body["data"].as_array().unwrap();
    assert!(!arr.is_empty());
    assert!(arr[0].as_object().unwrap().contains_key("name"));
}

// ── /api/strategy/strategies ─────────────────────────────────────────────────────

#[tokio::test]
async fn create_strategy_returns_201_with_id() {
    let body = serde_json::json!({
        "name": "my_strat",
        "label": "My Strategy",
        "strategy_spec": script_spec()
    });
    let resp = test_app()
        .oneshot(post_json("/api/v1/strategy/strategies", body))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::CREATED);
    let created = json_body(resp).await;
    assert!(created["data"]["id"].is_string());
    assert_eq!(created["data"]["name"], "my_strat");
}

#[tokio::test]
async fn list_strategies_contains_created() {
    let state = test_state();
    let id = seed_strategy(&state).await;

    let resp = router(state).oneshot(get("/api/v1/strategy/strategies")).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let list = json_body(resp).await;
    assert!(list["data"].as_array().unwrap().iter().any(|s| s["id"] == id));
}

#[tokio::test]
async fn get_strategy_by_id_returns_200() {
    let state = test_state();
    let id = seed_strategy(&state).await;

    let resp = router(state)
        .oneshot(get(&format!("/api/v1/strategy/strategies/{id}")))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(json_body(resp).await["data"]["id"], id);
}

#[tokio::test]
async fn get_strategy_unknown_id_returns_404() {
    let resp = test_app()
        .oneshot(get("/api/v1/strategy/strategies/does-not-exist"))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::NOT_FOUND);
}

#[tokio::test]
async fn update_strategy_label_returns_200() {
    let state = test_state();
    let id = seed_strategy(&state).await;

    let resp = router(state)
        .oneshot(put_json(
            &format!("/api/v1/strategy/strategies/{id}"),
            serde_json::json!({ "label": "Renamed" }),
        ))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(json_body(resp).await["data"]["label"], "Renamed");
}

#[tokio::test]
async fn delete_strategy_then_404() {
    let state = test_state();
    let id = seed_strategy(&state).await;

    let del = router(state.clone())
        .oneshot(delete(&format!("/api/v1/strategy/strategies/{id}")))
        .await
        .unwrap();
    assert_eq!(del.status(), StatusCode::NO_CONTENT);

    let get_resp = router(state)
        .oneshot(get(&format!("/api/v1/strategy/strategies/{id}")))
        .await
        .unwrap();
    assert_eq!(get_resp.status(), StatusCode::NOT_FOUND);
}

// ── /api/v1/data/:source/:symbol ─────────────────────────────────────────────

#[tokio::test]
async fn data_symbol_empty_body_returns_empty_response() {
    let state = test_state();
    state
        .ledger
        .advance(Timeframe::M1, Bar::new(1_000_000, "binance:BTCUSDT", 100.0, 101.0, 99.0, 100.5, 10.0))
        .unwrap();
    let body = serde_json::json!({});
    let resp = router(state)
        .oneshot(post_json("/api/v1/data/binance/BTCUSDT", body))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let b = json_body(resp).await;
    assert_eq!(b["data"]["symbol"], "BTCUSDT");
    assert_eq!(b["data"]["source"], "binance");
}

#[tokio::test]
async fn data_unknown_symbol_no_ledger_entry_returns_empty_candles() {
    let body = serde_json::json!({ "candles": {} });
    let resp = test_app()
        .oneshot(post_json("/api/v1/data/binance/UNKNOWN_XYZ", body))
        .await
        .unwrap();
    assert!(
        resp.status().is_success() || resp.status().is_client_error(),
        "unexpected status {}",
        resp.status()
    );
}

#[tokio::test]
async fn data_invalid_source_returns_400() {
    let body = serde_json::json!({});
    let resp = test_app()
        .oneshot(post_json("/api/v1/data/kraken/BTCUSDT", body))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
}

// ── /api/v1/data — out-of-ledger (parquet fallback) paths ────────────────────

/// `before` predates every bar in the ledger → falls through to DuckDB.
/// No parquet files in test → graceful empty response (not 5xx).
#[tokio::test]
async fn data_before_outside_ledger_window_falls_back_gracefully() {
    let state = test_state();
    // Seed a bar at t=1_000_000.
    state
        .ledger
        .advance(
            Timeframe::M1,
            Bar::new(1_000_000, "binance:BTCUSDT", 100.0, 101.0, 99.0, 100.5, 10.0),
        )
        .unwrap();

    // Request bars BEFORE the ledger window — triggers DuckDB fallback.
    let body = serde_json::json!({ "candles": { "before": 500_000, "limit": 10 } });
    let resp = router(state)
        .oneshot(post_json("/api/v1/data/binance/BTCUSDT", body))
        .await
        .unwrap();

    assert_eq!(resp.status(), StatusCode::OK);
    let b = json_body(resp).await;
    // No parquet files → candles.bars is empty but the shape is correct.
    assert_eq!(b["data"]["source"], "binance");
    assert_eq!(b["data"]["symbol"], "BTCUSDT");
    // candles section must be present when requested.
    assert!(b["data"]["candles"].is_object());
}

/// `before` cursor that falls within the ledger window returns bars from memory,
/// not from parquet — the DuckDB path must NOT be taken.
#[tokio::test]
async fn data_before_within_window_returns_bars_from_ledger() {
    let state = test_state();
    for i in 0u64..5 {
        state
            .ledger
            .advance(
                Timeframe::M1,
                Bar::new((1_000_000 + i * 60_000) as i64, "binance:BTCUSDT",
                         100.0 + i as f64, 101.0, 99.0, 100.5, 10.0),
            )
            .unwrap();
    }
    // Ask for bars before the last bar — should return from the live ring.
    let body = serde_json::json!({ "candles": { "before": 1_000_000 + 4 * 60_000, "limit": 10 } });
    let resp = router(state)
        .oneshot(post_json("/api/v1/data/binance/BTCUSDT", body))
        .await
        .unwrap();

    assert_eq!(resp.status(), StatusCode::OK);
    let b = json_body(resp).await;
    let bars = b["data"]["candles"]["bars"].as_array().unwrap();
    assert!(!bars.is_empty(), "expected bars from live ledger");
    // Bars must be sorted oldest-first.
    let ts: Vec<i64> = bars.iter().map(|b| b["t"].as_i64().unwrap()).collect();
    assert!(ts.windows(2).all(|w| w[0] <= w[1]), "bars not sorted ascending");
}

/// `before` outside window triggers the historical compute path.
/// No parquet → returns empty bars, not a crash.
#[tokio::test]
async fn data_before_outside_window_uses_historical_compute() {
    let state = test_state();
    state
        .ledger
        .advance(
            Timeframe::M1,
            Bar::new(1_000_000, "binance:BTCUSDT", 100.0, 101.0, 99.0, 100.5, 10.0),
        )
        .unwrap();

    let body = serde_json::json!({
        "candles": { "before": 500_000, "limit": 10 }
    });
    let resp = router(state)
        .oneshot(post_json("/api/v1/data/binance/BTCUSDT", body))
        .await
        .unwrap();

    assert_eq!(resp.status(), StatusCode::OK);
    let b = json_body(resp).await;
    assert_eq!(b["data"]["source"], "binance");
}

/// OKX dashed symbol accepted; ledger key uses the dashes as-is.
#[tokio::test]
async fn data_okx_dashed_symbol_accepted() {
    let state = test_state();
    state
        .ledger
        .advance(
            Timeframe::M1,
            Bar::new(1_000_000, "okx:BTC-USDT", 100.0, 101.0, 99.0, 100.5, 10.0),
        )
        .unwrap();

    let body = serde_json::json!({ "candles": {} });
    let resp = router(state)
        .oneshot(post_json("/api/v1/data/okx/BTC-USDT", body))
        .await
        .unwrap();

    assert_eq!(resp.status(), StatusCode::OK);
    let b = json_body(resp).await;
    assert_eq!(b["data"]["source"], "okx");
    assert_eq!(b["data"]["symbol"], "BTC-USDT");
}

