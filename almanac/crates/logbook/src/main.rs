use futures::StreamExt;

use std::env;
use std::path::PathBuf;
use std::sync::Arc;

use anyhow::Result;
use logbook::http::{router, AppState};
use logbook::runner;
use logbook::types::{BacktestRequest, ErrorResponse};
use tokio::sync::Semaphore;

// Maximum concurrent backtests (NATS + HTTP combined).
// Tune via MAX_CONCURRENT_BACKTESTS env var.
const DEFAULT_MAX_CONCURRENT: usize = 4;

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "logbook=info".into()),
        )
        .init();

    let nats_url = env::var("NATS_URL").unwrap_or_else(|_| "nats://localhost:4222".to_string());
    let http_addr = env::var("HTTP_ADDR").unwrap_or_else(|_| "0.0.0.0:8085".to_string());
    let data_dir: Arc<PathBuf> = Arc::new(PathBuf::from(
        env::var("DATA_DIR").unwrap_or_else(|_| "./data".to_string()),
    ));
    let max_concurrent: usize = env::var("MAX_CONCURRENT_BACKTESTS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(DEFAULT_MAX_CONCURRENT);

    let semaphore = Arc::new(Semaphore::new(max_concurrent));

    tracing::info!(
        data_dir = %data_dir.display(),
        http_addr = %http_addr,
        nats_url  = %nats_url,
        max_concurrent,
        "logbook starting"
    );

    // ── NATS connection ───────────────────────────────────────────────────────

    let nats_user = env::var("NATS_USER").ok();
    let nats_pass = env::var("NATS_PASS").ok();
    let opts = match (nats_user, nats_pass) {
        (Some(u), Some(p)) => async_nats::ConnectOptions::with_user_and_password(u, p),
        _ => async_nats::ConnectOptions::new(),
    };
    let nc = opts.connect(&nats_url).await?;

    // ── HTTP router ───────────────────────────────────────────────────────────

    let app_state = AppState {
        data_dir: Arc::clone(&data_dir),
        semaphore: Arc::clone(&semaphore),
    };
    let app = router(app_state);
    let listener = tokio::net::TcpListener::bind(&http_addr).await?;
    tracing::info!("HTTP listening on {}", http_addr);

    // ── Run both concurrently ─────────────────────────────────────────────────

    tokio::try_join!(
        run_nats(nc, Arc::clone(&data_dir), Arc::clone(&semaphore)),
        async {
            axum::serve(listener, app).await.map_err(anyhow::Error::from)
        },
    )?;

    Ok(())
}

// ── NATS subscriber loop ──────────────────────────────────────────────────────

async fn run_nats(
    nc: async_nats::Client,
    data_dir: Arc<PathBuf>,
    semaphore: Arc<Semaphore>,
) -> Result<()> {
    let mut sub = nc.subscribe("backtest.request").await?;
    tracing::info!("subscribed to backtest.request");

    while let Some(msg) = sub.next().await {
        let nc        = nc.clone();
        let data_dir  = Arc::clone(&data_dir);
        let semaphore = Arc::clone(&semaphore);

        tokio::spawn(async move {
            let reply = match msg.reply.clone() {
                Some(r) => r,
                None => {
                    tracing::warn!("backtest.request with no reply subject — ignoring");
                    return;
                }
            };

            let bytes = handle_nats(&msg.payload, &data_dir, &semaphore).await;
            if let Err(e) = nc.publish(reply, bytes.into()).await {
                tracing::error!("failed to publish backtest reply: {}", e);
            }
        });
    }

    Ok(())
}

async fn handle_nats(
    payload: &[u8],
    data_dir: &PathBuf,
    semaphore: &Semaphore,
) -> Vec<u8> {
    let req: BacktestRequest = match serde_json::from_slice(payload) {
        Ok(r) => r,
        Err(e) => {
            tracing::warn!("bad NATS request: {}", e);
            return error_json(&format!("invalid request: {}", e));
        }
    };

    // Respect concurrency limit — block here (not async) until a slot is free.
    let _permit = semaphore.acquire().await;

    tracing::info!(
        strategy = %req.strategy,
        symbol   = %req.symbol,
        from     = ?req.from,
        to       = ?req.to,
        "NATS backtest request"
    );

    let data_dir = data_dir.clone();
    let result = tokio::task::spawn_blocking(move || runner::run(req, &data_dir)).await;

    match result {
        Ok(Ok(resp)) => match serde_json::to_vec(&resp) {
            Ok(b)  => b,
            Err(e) => error_json(&format!("serialization error: {}", e)),
        },
        Ok(Err(e)) => {
            tracing::warn!("backtest failed: {:#}", e);
            error_json(&e.to_string())
        }
        Err(e) => {
            tracing::error!("spawn_blocking panicked: {}", e);
            error_json("internal error")
        }
    }
}

fn error_json(msg: &str) -> Vec<u8> {
    serde_json::to_vec(&ErrorResponse {
        error: msg.to_string(),
    })
    .unwrap_or_default()
}
