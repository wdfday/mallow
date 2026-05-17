mod feed;
mod handler;
mod ring;
mod symbols;
use alm_herald::{http, registry, watch_evaluator};
use feed::rest::{gap_fill_symbol, Exchange};

use std::sync::Arc;

use anyhow::Result;
use alm_core::Timeframe;
use alm_data::feed::BarFeed;
use alm_engine::data::{find_parquet_files, load_bars};
use alm_ledger::{Ledger, LedgerConfig, LedgerObserver};
use handler::Handler;
use registry::Registry;
use ring::BarRing;
use tokio::sync::{broadcast, mpsc};
use tracing::{info, warn};

#[tokio::main]
async fn main() -> Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "alm_herald=info,alm_ledger=info".into()),
        )
        .init();

    let nats_url = std::env::var("NATS_URL").unwrap_or_else(|_| "nats://localhost:4222".into());
    let (host_url, url_user, url_pass) = split_nats_userinfo(&nats_url);
    let nats_user = std::env::var("NATS_USER").ok().or(url_user);
    let nats_pass = std::env::var("NATS_PASS").ok().or(url_pass);

    let tf = std::env::var("HERALD_TF")
        .ok()
        .and_then(|s| parse_tf(&s))
        .unwrap_or(Timeframe::M1);

    info!(url = %host_url, user = ?nats_user, ?tf, "connecting to NATS");
    let client = match (nats_user.as_deref(), nats_pass.as_deref()) {
        (Some(u), Some(p)) => {
            async_nats::ConnectOptions::with_user_and_password(u.into(), p.into())
                .connect(&host_url)
                .await?
        }
        _ => async_nats::connect(&host_url).await?,
    };
    info!("connected to NATS");

    // ── Symbol config (load early — needed for bootstrap + warm-set) ─────────
    //
    // Priority:
    //   1. HERALD_SYMBOLS_FILE=/path/to/symbols.yaml  (preferred — shared with hist-data)
    //   2. HERALD_BINANCE_SYMBOLS / HERALD_OKX_SYMBOLS env vars (fallback)
    let sym_cfg = match symbols::SymbolConfig::from_env_file() {
        Ok(Some(cfg)) => {
            info!(
                binance = cfg.binance.len(),
                okx = cfg.okx.len(),
                "loaded symbols from HERALD_SYMBOLS_FILE"
            );
            cfg
        }
        Ok(None) => {
            let mut cfg = symbols::SymbolConfig::default();
            cfg.binance = parse_symbol_list("HERALD_BINANCE_SYMBOLS");
            cfg.okx     = parse_symbol_list("HERALD_OKX_SYMBOLS");
            cfg
        }
        Err(e) => {
            anyhow::bail!("failed to load symbols file: {e}");
        }
    };

    // ── Ledger + Registry ─────────────────────────────────────────────────────
    let ledger = Arc::new(Ledger::new(LedgerConfig::default()));

    // Each subscribed TF self-sources its own bar history (REST + WebSocket).
    // No resampler, no warm-set — indicators are computed lazily on demand.
    let startup_symbols: Vec<String> = match std::env::var("HERALD_SYMBOLS") {
        Ok(v) => v.split(',').map(|t| t.trim().to_string()).filter(|t| !t.is_empty()).collect(),
        Err(_) => sym_cfg.all_prefixed(),
    };

    // ── Data directory ────────────────────────────────────────────────────────
    let data_dir = Arc::new(std::path::PathBuf::from(
        std::env::var("HERALD_DATA_DIR")
            .or_else(|_| std::env::var("DATA_DIR"))
            .unwrap_or_else(|_| "./data".into()),
    ));

    // ── Bootstrap from parquet ────────────────────────────────────────────────
    if !startup_symbols.is_empty() {
        // HERALD_WARM_BARS: max M1 bars to load per symbol (1 bar = 1 min).
        // Default 5000 ≈ 3.5 days — enough to warm all built-in indicators.
        // Set to 0 to skip bootstrap entirely.
        let warm_bars: i64 = std::env::var("HERALD_WARM_BARS")
            .ok()
            .and_then(|s| s.parse().ok())
            .unwrap_or(5000);

        if warm_bars > 0 && data_dir.exists() {
            info!(symbols = startup_symbols.len(), warm_bars, "bootstrapping M1 from parquet");

            for sym in &startup_symbols {
                let (_, raw_sym) = crate::symbols::SymbolConfig::split_prefix(sym);
                let parquet_sym = raw_sym.replace('-', "");
                let files = find_parquet_files(&data_dir, &parquet_sym, Some("M1"), None);
                if files.is_empty() {
                    warn!(symbol = %sym, "no M1 parquet files — skipping bootstrap");
                    continue;
                }

                let now_ms = chrono::Utc::now().timestamp_millis();
                let from_ms = now_ms - warm_bars * 60_000;

                match load_bars(&files, &parquet_sym, Some(from_ms), None, false, "") {
                    Ok(mut feed) => {
                        let mut count = 0usize;
                        while let Some(mut bar) = feed.next() {
                            bar.symbol = sym.clone();
                            let _ = ledger.advance(tf, bar);
                            count += 1;
                        }
                        info!(symbol = %sym, bars = count, "parquet bootstrap done");
                    }
                    Err(e) => warn!(symbol = %sym, err = %e, "parquet bootstrap failed"),
                }
            }
        } else if warm_bars == 0 {
            info!("HERALD_WARM_BARS=0 — skipping bootstrap");
        } else {
            warn!(data_dir = %data_dir.display(), "data_dir not found — skipping bootstrap");
        }
    }

    // ── REST gap-fill ─────────────────────────────────────────────────────────
    // For the base TF (M1): close the gap from the last parquet bar to now.
    // For every other subscribed TF: fetch the last WINDOW_BARS bars directly.
    // Observers are NOT yet subscribed — no signals fire during gap-fill.
    if !startup_symbols.is_empty() {
        info!("starting REST gap-fill for {} symbol(s)", startup_symbols.len());
        for sym in &startup_symbols {
            let last_ts = ledger.with_state(sym, tf, |s| s.last_ts).flatten();
            // base_from_ms: one bar after the last parquet bar, or None if no parquet.
            let base_from_ms = last_ts.map(|ts| ts + tf.duration_ms());

            let (exchange_str, raw_sym) = crate::symbols::SymbolConfig::split_prefix(sym);
            let exchange = if exchange_str == "okx" { Exchange::Okx } else { Exchange::Binance };
            gap_fill_symbol(&ledger, tf, sym, raw_sym, exchange, base_from_ms, feed::SUBSCRIBE_TFS).await;
        }
    }

    let (sig_tx, sig_rx) = mpsc::unbounded_channel();
    let registry = Arc::new(Registry::with_default_fallback(ledger.clone(), tf, sig_tx));
    ledger.subscribe(registry.clone() as Arc<dyn LedgerObserver>);

    // ── Watch evaluator ───────────────────────────────────────────────────────
    let (watch_dispatch_tx, watch_dispatch_rx) = mpsc::unbounded_channel();
    // WatchStore is created inside HttpState; we need it before creating HttpState.
    // Use a temporary store here — HttpState will share the same Arc via watch_store below.
    let watch_store = http::watch::new_store();
    let watch_eval = Arc::new(watch_evaluator::WatchEvaluator::new(
        watch_store.clone(), ledger.clone(), tf, watch_dispatch_tx,
    ));
    ledger.subscribe(watch_eval.clone() as Arc<dyn LedgerObserver>);
    tokio::spawn(watch_evaluator::watch_dispatcher(client.clone(), watch_dispatch_rx));

    info!("ledger + registry + watch_evaluator wired");

    // ── 24h ring buffer ───────────────────────────────────────────────────────
    let ring = BarRing::new();

    // ── WebSocket ingesters ───────────────────────────────────────────────────
    let (bar_tx, bar_rx) = mpsc::unbounded_channel();

    if !sym_cfg.binance.is_empty() {
        info!(symbols = ?sym_cfg.binance, "starting Binance WebSocket ingester");
        feed::binance::spawn(sym_cfg.binance.clone(), bar_tx.clone());
    }
    if !sym_cfg.okx.is_empty() {
        info!(symbols = ?sym_cfg.okx, "starting OKX WebSocket ingester");
        feed::okx::spawn(sym_cfg.okx.clone(), bar_tx.clone());
    }
    if sym_cfg.binance.is_empty() && sym_cfg.okx.is_empty() {
        warn!("no symbols configured — herald will receive no live bars");
    }
    drop(bar_tx);

    // ── Store backend ─────────────────────────────────────────────────────────
    let store = match std::env::var("HERALD_DATABASE_URL") {
        Ok(db_url) => {
            info!("HERALD_DATABASE_URL set — connecting to PostgreSQL");
            let pool = sqlx::postgres::PgPoolOptions::new()
                .max_connections(10)
                .connect(&db_url)
                .await?;
            http::store::migrate::run(&pool).await?;
            http::StoreBackend::postgres(pool)
        }
        Err(_) => {
            info!("HERALD_DATABASE_URL unset — using in-memory store");
            http::StoreBackend::in_memory()
        }
    };

    // ── SSE broadcast channels ────────────────────────────────────────────────
    let (bar_bcast_tx, _) = broadcast::channel::<alm_core::Bar>(256);
    let (sig_bcast_tx, _) = broadcast::channel::<std::sync::Arc<registry::HandSignal>>(64);

    // ── HTTP server ───────────────────────────────────────────────────────────
    let http_addr = std::env::var("HERALD_HTTP_ADDR").unwrap_or_else(|_| "0.0.0.0:8090".into());
    let max_concurrent_bt: usize = std::env::var("HERALD_MAX_BACKTESTS")
        .ok().and_then(|s| s.parse().ok()).unwrap_or(4);
    info!(data_dir = %data_dir.display(), max_concurrent_bt, "configuring HTTP");

    let http_state = http::HttpState::new(
        ledger.clone(), tf, data_dir, max_concurrent_bt, store,
        bar_bcast_tx.clone(), sig_bcast_tx.clone(),
        watch_eval, watch_store,
    );
    http::watch::restore_from_store(&http_state).await;

    let router = http::router(http_state);
    let listener = tokio::net::TcpListener::bind(&http_addr).await?;
    info!(addr = %http_addr, "herald HTTP listening");
    let http_task = tokio::spawn(async move {
        if let Err(e) = axum::serve(listener, router).await {
            warn!(error = %e, "herald HTTP server exited");
        }
    });

    // ── Main loop ─────────────────────────────────────────────────────────────
    let handler = Handler::new(client, ledger, registry, ring, tf, bar_rx, sig_rx,
        bar_bcast_tx, sig_bcast_tx);
    let result = handler.run().await;
    http_task.abort();
    result?;

    Ok(())
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn parse_symbol_list(env_key: &str) -> Vec<String> {
    std::env::var(env_key)
        .ok()
        .map(|s| s.split(',').map(|t| t.trim().to_string()).filter(|t| !t.is_empty()).collect())
        .unwrap_or_default()
}

fn split_nats_userinfo(url: &str) -> (String, Option<String>, Option<String>) {
    let scheme_end = url.find("://").map(|i| i + 3).unwrap_or(0);
    let rest = &url[scheme_end..];
    let path_start = rest.find('/').map(|i| scheme_end + i).unwrap_or(url.len());
    let authority = &url[scheme_end..path_start];
    let Some(at_pos) = authority.find('@') else {
        return (url.to_string(), None, None);
    };
    let (userinfo, host) = authority.split_at(at_pos);
    let host = &host[1..];
    let (user, pass) = match userinfo.find(':') {
        Some(i) => (Some(userinfo[..i].to_string()), Some(userinfo[i + 1..].to_string())),
        None    => (Some(userinfo.to_string()), None),
    };
    let host_url = format!("{}{}{}", &url[..scheme_end], host, &url[path_start..]);
    (host_url, user, pass)
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
