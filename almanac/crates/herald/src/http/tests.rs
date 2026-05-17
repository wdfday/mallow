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
use tokio::sync::broadcast;

use crate::http::{router, HttpState, StoreBackend};
use crate::registry::HandSignal;
use crate::watch_evaluator::WatchEvaluator;

// ── Helpers ───────────────────────────────────────────────────────────────────

fn test_state() -> HttpState {
    let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
    let data_dir = Arc::new(PathBuf::from("/nonexistent-test-dir"));
    let (bar_tx, _) = broadcast::channel::<Bar>(256);
    let (sig_tx, _) = broadcast::channel::<Arc<HandSignal>>(64);
    let watch_store = crate::http::new_watch_store();
    let (dispatch_tx, _dispatch_rx) = tokio::sync::mpsc::unbounded_channel();
    let watch_eval = Arc::new(WatchEvaluator::new(
        watch_store.clone(),
        ledger.clone(),
        Timeframe::M1,
        dispatch_tx,
    ));
    HttpState::new(
        ledger,
        Timeframe::M1,
        data_dir,
        1,
        StoreBackend::in_memory(),
        bar_tx,
        sig_tx,
        watch_eval,
        watch_store,
    )
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
        .oneshot(post_json("/api/v1/store/strategies", body))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::CREATED, "seed_strategy: create failed");
    json_body(resp).await["data"]["id"].as_str().unwrap().to_owned()
}

/// Create a backtest case in `state` and return its UUID.
async fn seed_case(state: &HttpState, strategy_id: &str) -> String {
    let body = serde_json::json!({
        "label": "Seed Case",
        "strategy_id": strategy_id,
        "symbol": "BTCUSDT"
    });
    let resp = router(state.clone())
        .oneshot(post_json("/api/v1/store/cases", body))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::CREATED, "seed_case: create failed");
    json_body(resp).await["data"]["id"].as_str().unwrap().to_owned()
}

/// Create a watch entry in `state` and return its UUID.
async fn seed_watch(state: &HttpState) -> String {
    let body = serde_json::json!({
        "symbols": ["BTCUSDT"],
        "strategy_spec": script_spec(),
        "webhook_url": "http://localhost:9999/hook"
    });
    let resp = router(state.clone())
        .oneshot(post_json("/api/v1/watch", body))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::CREATED, "seed_watch: create failed");
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
    let group = &body["data"]["binance"];
    let arr = group.as_array().unwrap();
    assert_eq!(arr.len(), 1);
    // Prefix is stripped — only the raw ticker reaches the wire.
    assert_eq!(arr[0]["symbol"], "BTCUSDT");
    assert_eq!(arr[0]["tf"], "M1");
    assert_eq!(arr[0]["bars"], 1);
}

#[tokio::test]
async fn symbols_without_indicators_flag_omits_field() {
    let state = test_state();
    state
        .ledger
        .advance(Timeframe::M1, Bar::new(1_000_000, "binance:ETHUSDT", 50.0, 51.0, 49.0, 50.5, 5.0))
        .unwrap();

    let resp = router(state)
        .oneshot(get("/api/v1/symbols?indicators=false"))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let body = json_body(resp).await;
    let item = &body["data"]["binance"].as_array().unwrap()[0];
    assert!(!item.as_object().unwrap().contains_key("indicators"));
}

#[tokio::test]
async fn symbols_with_indicators_flag_includes_array() {
    let state = test_state();
    state
        .ledger
        .advance(Timeframe::M1, Bar::new(1_000_000, "binance:ETHUSDT", 50.0, 51.0, 49.0, 50.5, 5.0))
        .unwrap();

    let resp = router(state)
        .oneshot(get("/api/v1/symbols?indicators=true"))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let body = json_body(resp).await;
    let item = &body["data"]["binance"].as_array().unwrap()[0];
    assert!(item["indicators"].is_array());
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

// ── /api/store/strategies ─────────────────────────────────────────────────────

#[tokio::test]
async fn create_strategy_returns_201_with_id() {
    let body = serde_json::json!({
        "name": "my_strat",
        "label": "My Strategy",
        "strategy_spec": script_spec()
    });
    let resp = test_app()
        .oneshot(post_json("/api/v1/store/strategies", body))
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

    let resp = router(state).oneshot(get("/api/v1/store/strategies")).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let list = json_body(resp).await;
    assert!(list["data"].as_array().unwrap().iter().any(|s| s["id"] == id));
}

#[tokio::test]
async fn get_strategy_by_id_returns_200() {
    let state = test_state();
    let id = seed_strategy(&state).await;

    let resp = router(state)
        .oneshot(get(&format!("/api/v1/store/strategies/{id}")))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(json_body(resp).await["data"]["id"], id);
}

#[tokio::test]
async fn get_strategy_unknown_id_returns_404() {
    let resp = test_app()
        .oneshot(get("/api/v1/store/strategies/does-not-exist"))
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
            &format!("/api/v1/store/strategies/{id}"),
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
        .oneshot(delete(&format!("/api/v1/store/strategies/{id}")))
        .await
        .unwrap();
    assert_eq!(del.status(), StatusCode::NO_CONTENT);

    let get_resp = router(state)
        .oneshot(get(&format!("/api/v1/store/strategies/{id}")))
        .await
        .unwrap();
    assert_eq!(get_resp.status(), StatusCode::NOT_FOUND);
}

// ── /api/store/cases ──────────────────────────────────────────────────────────

#[tokio::test]
async fn create_case_valid_strategy_id_returns_201() {
    let state = test_state();
    let strategy_id = seed_strategy(&state).await;

    let body = serde_json::json!({
        "label": "Test Case",
        "strategy_id": strategy_id,
        "symbol": "BTCUSDT"
    });
    let resp = router(state)
        .oneshot(post_json("/api/v1/store/cases", body))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::CREATED);
    let created = json_body(resp).await;
    assert!(created["data"]["id"].is_string());
    assert_eq!(created["data"]["symbol"], "BTCUSDT");
}

#[tokio::test]
async fn create_case_unknown_strategy_id_rejected() {
    let body = serde_json::json!({
        "label": "Bad Case",
        "strategy_id": "nonexistent-uuid",
        "symbol": "BTCUSDT"
    });
    let resp = test_app()
        .oneshot(post_json("/api/v1/store/cases", body))
        .await
        .unwrap();
    assert!(resp.status().is_client_error());
}

#[tokio::test]
async fn get_case_by_id_returns_200() {
    let state = test_state();
    let strategy_id = seed_strategy(&state).await;
    let case_id = seed_case(&state, &strategy_id).await;

    let resp = router(state)
        .oneshot(get(&format!("/api/v1/store/cases/{case_id}")))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(json_body(resp).await["data"]["id"], case_id);
}

#[tokio::test]
async fn run_case_no_data_returns_error_not_panic() {
    let state = test_state();
    let strategy_id = seed_strategy(&state).await;
    let case_id = seed_case(&state, &strategy_id).await;

    // data_dir points to /nonexistent-test-dir, so run must fail gracefully
    let resp = router(state)
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/api/v1/store/cases/{case_id}/run"))
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert!(
        resp.status().is_client_error() || resp.status().is_server_error(),
        "expected 4xx/5xx, got {}",
        resp.status()
    );
    let body = json_body(resp).await;
    assert!(body["message"].is_string(), "response should contain message field");
}

#[tokio::test]
async fn delete_case_returns_204() {
    let state = test_state();
    let strategy_id = seed_strategy(&state).await;
    let case_id = seed_case(&state, &strategy_id).await;

    let resp = router(state)
        .oneshot(delete(&format!("/api/v1/store/cases/{case_id}")))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::NO_CONTENT);
}

// ── /api/watch ────────────────────────────────────────────────────────────────

#[tokio::test]
async fn create_watch_returns_201_with_id() {
    let state = test_state();
    let id = seed_watch(&state).await;
    assert!(!id.is_empty());
}

#[tokio::test]
async fn list_watches_contains_created() {
    let state = test_state();
    let id = seed_watch(&state).await;

    let resp = router(state).oneshot(get("/api/v1/watch")).await.unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let list = json_body(resp).await;
    assert!(list["data"].as_array().unwrap().iter().any(|w| w["id"] == id));
}

#[tokio::test]
async fn get_watch_by_id_returns_200() {
    let state = test_state();
    let id = seed_watch(&state).await;

    let resp = router(state)
        .oneshot(get(&format!("/api/v1/watch/{id}")))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(json_body(resp).await["data"]["id"], id);
}

#[tokio::test]
async fn delete_watch_then_404() {
    let state = test_state();
    let id = seed_watch(&state).await;

    let del = router(state.clone())
        .oneshot(delete(&format!("/api/v1/watch/{id}")))
        .await
        .unwrap();
    assert_eq!(del.status(), StatusCode::NO_CONTENT);

    let get_resp = router(state)
        .oneshot(get(&format!("/api/v1/watch/{id}")))
        .await
        .unwrap();
    assert_eq!(get_resp.status(), StatusCode::NOT_FOUND);
}

// ── /api/v1/data/:symbol ──────────────────────────────────────────────────────

#[tokio::test]
async fn data_symbol_empty_body_returns_empty_response() {
    let state = test_state();
    state
        .ledger
        .advance(Timeframe::M1, Bar::new(1_000_000, "BTCUSDT", 100.0, 101.0, 99.0, 100.5, 10.0))
        .unwrap();
    let body = serde_json::json!({});
    let resp = router(state)
        .oneshot(post_json("/api/v1/data/BTCUSDT", body))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let b = json_body(resp).await;
    assert_eq!(b["data"]["symbol"], "BTCUSDT");
}

#[tokio::test]
async fn data_unknown_symbol_no_ledger_entry_returns_empty_candles() {
    let body = serde_json::json!({ "candles": {} });
    let resp = test_app()
        .oneshot(post_json("/api/v1/data/UNKNOWN_XYZ", body))
        .await
        .unwrap();
    // No ledger entry and no parquet dir → returns empty candles (not 5xx).
    assert!(
        resp.status().is_success() || resp.status().is_client_error(),
        "unexpected status {}",
        resp.status()
    );
}

#[tokio::test]
async fn data_too_many_indicators_rejected() {
    let indicators: Vec<serde_json::Value> = (0..17)
        .map(|i| serde_json::json!({"type": "sma", "period": i + 2}))
        .collect();
    let body = serde_json::json!({ "indicators": indicators });
    let resp = test_app()
        .oneshot(post_json("/api/v1/data/BTCUSDT", body))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
    assert!(json_body(resp).await["message"].is_string());
}

// ── SSE smoke tests ───────────────────────────────────────────────────────────

#[tokio::test]
async fn sse_bar_stream_content_type_is_event_stream() {
    let resp = test_app()
        .oneshot(post_json("/api/v1/stream/BTCUSDT", serde_json::json!({})))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let ct = resp
        .headers()
        .get("content-type")
        .expect("content-type header missing")
        .to_str()
        .unwrap();
    assert!(ct.contains("text/event-stream"), "unexpected content-type: {ct}");
}

#[tokio::test]
async fn sse_signals_stream_content_type_is_event_stream() {
    let resp = test_app()
        .oneshot(get("/api/v1/stream/signals"))
        .await
        .unwrap();
    assert_eq!(resp.status(), StatusCode::OK);
    let ct = resp
        .headers()
        .get("content-type")
        .expect("content-type header missing")
        .to_str()
        .unwrap();
    assert!(ct.contains("text/event-stream"), "unexpected content-type: {ct}");
}
