// jemalloc — returns freed pages to OS aggressively after startup spikes
// (glibc malloc holds virtual memory indefinitely after HTF gap-fill allocs).
#[global_allocator]
static ALLOC: tikv_jemallocator::Jemalloc = tikv_jemallocator::Jemalloc;

mod feed;
mod handler;
mod ring;
mod symbols;
use alm_herald::{http, registry};
use metrics_exporter_prometheus::PrometheusBuilder;
use pyroscope_pprofrs::{pprof_backend, PprofConfig};
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
use tokio::sync::mpsc;
use tracing::{info, warn};
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;

#[tokio::main]
async fn main() -> Result<()> {
    let env_filter = tracing_subscriber::EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| "alm_herald=info,alm_ledger=info".into());

    let fmt_layer = tracing_subscriber::fmt::layer();

    // ── OpenTelemetry OTLP tracing (optional) ─────────────────────────────────
    // Only enabled when OTEL_EXPORTER_OTLP_ENDPOINT is set and non-empty.
    // If the env var is absent or empty, herald runs without OTLP tracing.
    let otel_endpoint = std::env::var("OTEL_EXPORTER_OTLP_ENDPOINT")
        .ok()
        .filter(|s| !s.is_empty());

    if let Some(ref endpoint) = otel_endpoint {
        match init_tracer(endpoint) {
            Ok(tracer) => {
                let otel_layer = tracing_opentelemetry::layer().with_tracer(tracer);
                tracing_subscriber::registry()
                    .with(env_filter)
                    .with(fmt_layer)
                    .with(otel_layer)
                    .init();
                info!(endpoint = %endpoint, "OpenTelemetry OTLP tracing enabled");
            }
            Err(e) => {
                // Fall back to plain logging if OTLP setup fails — don't crash.
                tracing_subscriber::registry()
                    .with(env_filter)
                    .with(fmt_layer)
                    .init();
                warn!(err = %e, "OTLP tracer init failed — falling back to plain logging");
            }
        }
    } else {
        tracing_subscriber::registry()
            .with(env_filter)
            .with(fmt_layer)
            .init();
    };

    // Install Prometheus recorder globally. Must happen before any metrics
    // are emitted. The handle is shared with the HTTP layer for rendering.
    let prometheus_handle = PrometheusBuilder::new()
        .install_recorder()
        .expect("failed to install Prometheus recorder");

    // ── Pyroscope continuous profiling ────────────────────────────────────────
    // CPU profiling via pprof-rs (100 Hz). Enabled when PYROSCOPE_URL is set.
    let _pyroscope_agent = if let Ok(url) = std::env::var("PYROSCOPE_URL") {
        if url.is_empty() {
            None
        } else {
            let app_name = "herald".to_string();
            let agent = pyroscope::PyroscopeAgent::builder(&url, &app_name)
                .backend(pprof_backend(PprofConfig::new().sample_rate(100)))
                .build()
                .expect("failed to build Pyroscope agent");
            let running = agent.start().expect("failed to start Pyroscope agent");
            info!(url = %url, "Pyroscope profiling active");
            Some(running)
        }
    } else {
        None
    };

    let nats_url = std::env::var("NATS_URL").unwrap_or_else(|_| "nats://localhost:4222".into());
    let (host_url, url_user, url_pass) = split_nats_userinfo(&nats_url);
    let nats_user = std::env::var("NATS_USER").ok().or(url_user);
    let nats_pass = std::env::var("NATS_PASS").ok().or(url_pass);

    let tf = std::env::var("HERALD_TF")
        .ok()
        .and_then(|s| s.parse().ok())
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
    metrics::gauge!("herald_nats_connected").set(1.0);

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
    // Loads historical bars before any observer is subscribed — no signals fire
    // during bootstrap (registry is not yet wired to the ledger).
    if !startup_symbols.is_empty() {
        // HERALD_WARM_BARS: max M1 bars to load per symbol (1 bar = 1 min).
        // Default 2000 ≈ 33h, sized to match the M1 ledger capacity in
        // `LedgerConfig::default()`. Higher values are silently capped by the
        // ledger's sliding window, so the extra disk I/O is wasted.
        // Set to 0 to skip bootstrap entirely.
        let warm_bars: i64 = std::env::var("HERALD_WARM_BARS")
            .ok()
            .and_then(|s| s.parse().ok())
            .unwrap_or(2000);

        if warm_bars > 0 && data_dir.exists() {
            info!(symbols = startup_symbols.len(), warm_bars, "bootstrapping M1 from parquet");

            let now_ms = chrono::Utc::now().timestamp_millis();
            let from_ms = now_ms - warm_bars * 60_000;
            // Daily parquet files contain ~1440 M1 bars each.
            let files_needed = (warm_bars as usize / 1440) + 2;

            use rayon::prelude::*;
            startup_symbols.par_iter().for_each(|sym| {
                let (_, raw_sym) = crate::symbols::SymbolConfig::split_prefix(sym);
                let parquet_sym = raw_sym.replace('-', "");
                let all_files = find_parquet_files(&data_dir, &parquet_sym, Some("M1"), None);
                if all_files.is_empty() {
                    warn!(symbol = %sym, "no M1 parquet files — skipping bootstrap");
                    return;
                }
                let files: Vec<_> = all_files.into_iter().rev().take(files_needed).rev().collect();
                match load_bars(&files, &parquet_sym, Some(from_ms), None, false, "") {
                    Ok(mut feed) => {
                        let bars = std::iter::from_fn(move || {
                            feed.next().map(|mut bar| { bar.symbol = sym.clone(); bar })
                        });
                        match ledger.bootstrap_symbol(sym, tf, bars) {
                            Ok(rep) => info!(symbol = %sym, bars = rep.fed, "parquet bootstrap done"),
                            Err(e) => warn!(symbol = %sym, err = %e, "parquet bootstrap failed"),
                        }
                    }
                    Err(e) => warn!(symbol = %sym, err = %e, "parquet bootstrap failed"),
                }
            });
        } else if warm_bars == 0 {
            info!("HERALD_WARM_BARS=0 — skipping bootstrap");
        } else {
            warn!(data_dir = %data_dir.display(), "data_dir not found — skipping bootstrap");
        }
    }

    // ── ResampleManager ───────────────────────────────────────────────────────
    //
    // Drives base→HTF aggregation for hands that need TFs the WS feed doesn't
    // publish directly. Subscribed BEFORE the registry so HTF advances appear
    // in the observer fan-out by the time hands react to them.
    let resample_mgr = alm_herald::helper::resample::ResampleManager::new(Arc::downgrade(&ledger));
    ledger.subscribe(resample_mgr.clone() as Arc<dyn LedgerObserver>);

    let (sig_tx, sig_rx) = mpsc::unbounded_channel();
    let registry = Arc::new(Registry::with_default_scripts(
        ledger.clone(), resample_mgr.clone(), tf, sig_tx,
        default_live_scripts(),
    ));
    ledger.subscribe(registry.clone() as Arc<dyn LedgerObserver>);

    info!("ledger + registry wired");

    // ── Store backend ─────────────────────────────────────────────────────────
    let db_url = std::env::var("HERALD_DATABASE_URL")
        .unwrap_or_else(|_| "postgres://mallow:mallow-dev@localhost:5432/herald?sslmode=disable".into());
    info!("connecting to PostgreSQL: {}", db_url);
    let pool = sqlx::postgres::PgPoolOptions::new()
        .max_connections(10)
        .connect(&db_url)
        .await?;
    http::strategy::migrate::run(&pool).await?;
    let store = http::StoreBackend::postgres(pool);

    // ── WS latency tracker — shared between Handler and HTTP ─────────────────
    let ws_latency = std::sync::Arc::new(alm_herald::WsLatencyTracker::new());

    // ── HTTP server ───────────────────────────────────────────────────────────
    // Bind and start serving immediately so the container responds to health
    // checks during warm-up.  The `/health` endpoint returns 503 until `ready`
    // is set to true after REST gap-fill completes.
    let http_addr = std::env::var("HERALD_HTTP_ADDR").unwrap_or_else(|_| "0.0.0.0:8090".into());
    let max_concurrent_bt: usize = std::env::var("HERALD_MAX_BACKTESTS")
        .ok().and_then(|s| s.parse().ok()).unwrap_or(4);
    info!(data_dir = %data_dir.display(), max_concurrent_bt, "configuring HTTP");

    let (http_state, ready) = http::HttpState::new(
        ledger.clone(), tf, data_dir, max_concurrent_bt, store,
        Arc::clone(&ws_latency),
        prometheus_handle,
    );

    let router = http::router(http_state);
    let listener = tokio::net::TcpListener::bind(&http_addr).await?;
    info!(addr = %http_addr, "herald HTTP listening (ready=false, /health → 503 until gap-fill done)");
    let http_task = tokio::spawn(async move {
        if let Err(e) = axum::serve(listener, router).await {
            warn!(error = %e, "herald HTTP server exited");
        }
    });

    // ── 24h ring buffer ───────────────────────────────────────────────────────
    let ring = BarRing::new();

    // ── WebSocket ingesters (start before gap-fill) ───────────────────────────
    // Bars that close during gap-fill queue in the unbounded channel.
    // handler.run() drains them after gap-fill; SymbolState::advance() silently
    // skips any duplicates that overlap with REST-fetched bars.
    let (bar_tx, bar_rx) = mpsc::unbounded_channel();

    if !sym_cfg.binance.is_empty() {
        info!(symbols = ?sym_cfg.binance, "starting Binance WebSocket ingester");
        feed::binance::spawn(sym_cfg.binance.clone(), bar_tx.clone(), ledger.clone(), tf);
    }
    if !sym_cfg.okx.is_empty() {
        info!(symbols = ?sym_cfg.okx, "starting OKX WebSocket ingester");
        feed::okx::spawn(sym_cfg.okx.clone(), bar_tx.clone(), ledger.clone(), tf);
    }
    if sym_cfg.binance.is_empty() && sym_cfg.okx.is_empty() {
        warn!("no symbols configured — herald will receive no live bars");
    }
    drop(bar_tx);

    // ── REST gap-fill ─────────────────────────────────────────────────────────
    // For the base TF (M1): close the gap from the last parquet bar to now.
    // For every other subscribed TF: fetch the last WINDOW_BARS bars directly.
    //
    // OKX REST rate limit is 20 req/2s for public endpoints. Each symbol
    // fetches one request per subscribed TF serially, so running all OKX
    // symbols in parallel easily triggers 429s. We use a semaphore to cap
    // concurrent OKX gap-fills at 3. Binance is more generous — run parallel.
    if !startup_symbols.is_empty() {
        // OKX: max 3 concurrent gap-fills
        const OKX_CONCURRENCY: usize = 3;
        let okx_sem = Arc::new(tokio::sync::Semaphore::new(OKX_CONCURRENCY));

        info!("starting REST gap-fill for {} symbol(s)", startup_symbols.len());
        let gap_tasks: Vec<_> = startup_symbols.iter().map(|sym| {
            let ledger = ledger.clone();
            let sym = sym.clone();
            let last_ts = ledger.with_state(&sym, tf, |s| s.last_ts).flatten();
            let base_from_ms = last_ts.map(|ts| ts + tf.duration_ms());
            let (exchange_str, raw_sym) = crate::symbols::SymbolConfig::split_prefix(&sym);
            let exchange = if exchange_str == "okx" { Exchange::Okx } else { Exchange::Binance };
            let raw_sym = raw_sym.to_string();
            let okx_sem = okx_sem.clone();
            tokio::spawn(async move {
                let _permit = if matches!(exchange, Exchange::Okx) {
                    Some(okx_sem.acquire_owned().await.expect("semaphore closed"))
                } else {
                    None
                };
                gap_fill_symbol(&ledger, tf, &sym, &raw_sym, exchange, base_from_ms, feed::SUBSCRIBE_TFS).await;
            })
        }).collect();
        futures::future::join_all(gap_tasks).await;
    }

    // Mark service ready — /health now returns 200 OK.
    ready.store(true, std::sync::atomic::Ordering::Relaxed);
    info!("gap-fill complete — herald ready (ready=true, /health → 200 OK)");

    // ── Main loop ─────────────────────────────────────────────────────────────
    let handler = Handler::new(client, ledger, registry, ring, tf, bar_rx, sig_rx,
        ws_latency);
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


// ── OpenTelemetry OTLP initialiser ────────────────────────────────────────────

fn init_tracer(endpoint: &str) -> Result<opentelemetry_sdk::trace::Tracer, opentelemetry::trace::TraceError> {
    use opentelemetry::trace::TracerProvider as _;
    use opentelemetry_otlp::WithExportConfig;
    use opentelemetry_sdk::trace::{BatchSpanProcessor, TracerProvider};

    let exporter = opentelemetry_otlp::SpanExporter::builder()
        .with_tonic()
        .with_endpoint(endpoint)
        .build()?;

    let processor = BatchSpanProcessor::builder(exporter, opentelemetry_sdk::runtime::Tokio).build();

    let resource = opentelemetry_sdk::Resource::new(vec![
        opentelemetry::KeyValue::new("service.name", "herald"),
    ]);

    let provider = TracerProvider::builder()
        .with_span_processor(processor)
        .with_resource(resource)
        .build();

    let tracer = provider.tracer("herald");

    // Set this provider as the global tracer provider so that spans are exported.
    opentelemetry::global::set_tracer_provider(provider);

    Ok(tracer)
}

/// Default live strategies seeded into every unregistered symbol.
/// Mix of v1 (base-TF only) and v2 (MTF with H1/H4 context).
fn default_live_scripts() -> Vec<String> {
    vec![
//         // ── v1: base-TF only ─────────────────────────────────────────────
//         // 0. EMA cross 20/50
//         r#"let fast = ind.ema(20);
// let slow = ind.ema(50);
// let long = cross_above(fast, slow);
// let exit = cross_below(fast, slow);"#.into(),
//
//         // 1. RSI mean reversion
//         r#"let rsi = ind.rsi(14);
// let long = rsi[0] < 30.0;
// let short = rsi[0] > 70.0;
// let exit = (rsi[1] < 50.0 && rsi[0] >= 50.0) || (rsi[1] > 50.0 && rsi[0] <= 50.0);"#.into(),
//
//         // 2. MACD histogram flip
//         r#"let macd = ind.macd(12, 26, 9);
// let long = macd[1].histogram < 0.0 && macd[0].histogram >= 0.0;
// let exit = macd[1].histogram > 0.0 && macd[0].histogram <= 0.0;"#.into(),
//
//         // 3. Bollinger band mean reversion
//         r#"let bb = ind.bbands(20);
// let rsi = ind.rsi(14);
// let long = close[0] < bb[0].lower && rsi[0] < 35.0;
// let exit = close[0] > bb[0].middle;"#.into(),
//
//         // 4. SuperTrend direction flip
//         r#"let st = ind.supertrend(10, 3.0);
// let long = st[1].direction < 0.0 && st[0].direction >= 0.0;
// let exit = st[0].direction < 0.0;"#.into(),
//
//         // ── v2: MTF (H1 / H4 context + M1 entry) ────────────────────────
//         // 5. H1 EMA trend + M1 EMA cross entry
//         r#"let h1_ema = ind.ema(50, "H1");
// let fast = ind.ema(8);
// let slow = ind.ema(21);
// let trend_up = close[0] > h1_ema[0];
// let long = trend_up && cross_above(fast, slow);
// let exit = cross_below(fast, slow) || close[0] < h1_ema[0];"#.into(),
//
//         // 6. H1 RSI filter + M1 RSI oversold entry
//         r#"let h1_rsi = ind.rsi(14, "H1");
// let rsi = ind.rsi(7);
// let h1_bull = h1_rsi[0] > 50.0;
// let long = h1_bull && rsi[0] < 30.0;
// let exit = rsi[0] > 65.0;"#.into(),
//
//         // 7. H4 ADX trending + M1 EMA cross entry
//         r#"let h4_adx = ind.adx(14, "H4");
// let fast = ind.ema(8);
// let slow = ind.ema(21);
// let trending = h4_adx[0].adx > 25.0 && h4_adx[0].di_plus > h4_adx[0].di_minus;
// let long = trending && cross_above(fast, slow);
// let exit = cross_below(fast, slow);"#.into(),
//
//         // 8. H1 MACD + M1 RSI pullback
//         r#"let h1_macd = ind.macd(12, 26, 9, "H1");
// let rsi = ind.rsi(14);
// let h1_bull = h1_macd[0].histogram > 0.0;
// let long = h1_bull && rsi[0] < 40.0;
// let exit = !h1_bull || rsi[0] > 65.0;"#.into(),
//
//         // 9. H1 BB position + M1 EMA momentum
//         r#"let h1_bb = ind.bbands(20, "H1");
// let ema = ind.ema(21);
// let above_mid = close[0] > h1_bb[0].middle;
// let long = above_mid && close[0] > ema[0] && close[1] <= ema[1];
// let exit = close[0] < h1_bb[0].middle;"#.into(),
    ]
}
