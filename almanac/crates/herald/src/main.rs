mod handler;
mod http;
mod registry;

use std::sync::Arc;

use anyhow::Result;
use alm_core::Timeframe;
use alm_ledger::{default_warm_set, Ledger, LedgerConfig, LedgerObserver};
use async_nats::jetstream::consumer::{pull, AckPolicy};
use handler::Handler;
use registry::Registry;
use tokio::sync::mpsc;
use tracing::{info, warn};

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "herald=info,alm_ledger=info".into()),
        )
        .init();

    let nats_url = std::env::var("NATS_URL").unwrap_or_else(|_| "nats://localhost:4222".into());

    // Herald operates at one timeframe — default M1 (matches stream-data
    // publisher default). Override with HERALD_TF=M5 / H1 / etc.
    let tf = std::env::var("HERALD_TF")
        .ok()
        .and_then(|s| parse_tf(&s))
        .unwrap_or(Timeframe::M1);

    info!(url = %nats_url, ?tf, "connecting to NATS");
    let client = async_nats::connect(&nats_url).await?;
    info!("connected to NATS");

    // ── Ledger + Registry wiring ──────────────────────────────────────────
    let ledger = Arc::new(Ledger::new(LedgerConfig::default()));

    // Pre-register the default warm-set for every startup symbol BEFORE
    // any observer subscribes and BEFORE bootstrap runs. This keeps
    // historical indicator computation consistent across indicators and
    // makes scalar overlays instantly available on first HTTP request.
    //
    // HERALD_SYMBOLS="BTCUSDT,ETHUSDT,AAPL" — empty = no pre-warm, every
    // symbol gets lazily-registered state on its first live bar.
    let startup_symbols: Vec<String> = std::env::var("HERALD_SYMBOLS")
        .ok()
        .map(|s| {
            s.split(',')
                .map(|t| t.trim().to_string())
                .filter(|t| !t.is_empty())
                .collect()
        })
        .unwrap_or_default();

    if !startup_symbols.is_empty() {
        let warm = default_warm_set();
        info!(
            symbols = startup_symbols.len(),
            indicators = warm.len(),
            ?tf,
            "applying default warm-set"
        );
        for sym in &startup_symbols {
            let (ok, skipped) = ledger.apply_warm_set(sym, tf, warm.iter().cloned());
            if skipped > 0 {
                warn!(symbol = %sym, registered = ok, skipped, "warm-set partially applied");
            }
        }
    } else {
        info!("HERALD_SYMBOLS unset — skipping warm-set pre-registration");
    }

    // TODO(bootstrap_parquet): feed historical bars from parquet here,
    // BEFORE the observer is attached below, so replayed warm-up bars do
    // not trigger the bot registry.

    let (sig_tx, sig_rx) = mpsc::unbounded_channel();
    let registry = Arc::new(Registry::with_default_fallback(ledger.clone(), tf, sig_tx));
    ledger.subscribe(registry.clone() as Arc<dyn LedgerObserver>);
    info!("ledger + registry wired");

    // ── JetStream BARS consumer ───────────────────────────────────────────
    let js = async_nats::jetstream::new(client.clone());
    let stream = js.get_stream("BARS").await?;
    let consumer = stream
        .get_or_create_consumer(
            "herald",
            pull::Config {
                durable_name: Some("herald".into()),
                filter_subject: "bars.>".into(),
                ack_policy: AckPolicy::Explicit,
                ..Default::default()
            },
        )
        .await?;
    info!("JetStream pull consumer 'herald' ready on BARS stream");

    // ── HTTP server ───────────────────────────────────────────────────────
    //
    // Single HTTP surface for both live data (ledger-backed) and batch
    // backtests (dispatched to `alm_engine::backtest`). Kept in-process so
    // live handlers read from the same state the NATS consumer writes to —
    // no serialization hop and no second copy of the state machine.
    let http_addr = std::env::var("HERALD_HTTP_ADDR")
        .unwrap_or_else(|_| "0.0.0.0:8090".into());
    let data_dir = Arc::new(std::path::PathBuf::from(
        std::env::var("HERALD_DATA_DIR")
            .or_else(|_| std::env::var("DATA_DIR"))
            .unwrap_or_else(|_| "./data".into()),
    ));
    let max_concurrent_bt: usize = std::env::var("HERALD_MAX_BACKTESTS")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(4);
    info!(
        data_dir = %data_dir.display(),
        max_concurrent_bt,
        "configuring HTTP backtest dispatcher",
    );
    let http_state = http::HttpState::new(ledger.clone(), tf, data_dir, max_concurrent_bt);
    let router = http::router(http_state);
    let listener = tokio::net::TcpListener::bind(&http_addr).await?;
    info!(addr = %http_addr, "herald HTTP listening");
    let http_task = tokio::spawn(async move {
        if let Err(e) = axum::serve(listener, router).await {
            warn!(error = %e, "herald HTTP server exited");
        }
    });

    // ── JetStream consumer loop ───────────────────────────────────────────
    let handler = Handler::new(client, consumer, ledger, registry, tf, sig_rx);
    let result = handler.run().await;
    http_task.abort();
    result?;

    Ok(())
}

fn parse_tf(s: &str) -> Option<Timeframe> {
    match s.to_ascii_uppercase().as_str() {
        "M1"  => Some(Timeframe::M1),
        "M5"  => Some(Timeframe::M5),
        "M15" => Some(Timeframe::M15),
        "M30" => Some(Timeframe::M30),
        "H1"  => Some(Timeframe::H1),
        "H4"  => Some(Timeframe::H4),
        "D1"  => Some(Timeframe::D1),
        "W1"  => Some(Timeframe::W1),
        _ => None,
    }
}
