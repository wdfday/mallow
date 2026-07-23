//! Hand-capacity stress test — simulates registering N hands on the live
//! `Registry` and replaying synthetic bars through the real
//! `evaluate_and_publish` hot path (via `Ledger::advance`, exactly as
//! production WS bars do), reporting per-tick latency percentiles against
//! the timeframe's real-time budget.
//!
//! Two phases:
//!   1. Timed run — `bars` synthetic ticks, one `Ledger::advance` per symbol
//!      per tick (matches `SymbolGroup::evaluate_all`'s sequential design —
//!      today all symbols are driven from the same tokio task). Reports
//!      p50/p95/p99/max tick latency vs the timeframe budget.
//!   2. Optional flamegraph pass (`--flamegraph`) — same workload rebuilt
//!      from scratch and replayed again under a CPU profiler so sampling
//!      overhead never pollutes the phase-1 numbers.
//!
//! Usage:
//!   cargo run --release --bin hand-stress -- --hands 5000 --symbols 1 --bars 300
//!   cargo run --release --bin hand-stress -- --hands 5000 --flamegraph --out flame.svg
//!
//! Flags:
//!   --hands N        total hands to register (default 1000)
//!   --symbols N      spread hands round-robin across N symbols (default 1 — worst case:
//!                    all hands serialize behind one SymbolGroup mutex)
//!   --bars N         simulated bar ticks to replay after registration (default 300)
//!   --tf TF          M1|M5|M15|M30|H1|H4|D1|W1 (default M1)
//!   --flamegraph     capture a CPU flamegraph of the replay loop
//!   --out PATH       flamegraph output path (default flamegraph.svg)

use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

// `#[global_allocator]` is per-binary, not per-crate — main.rs's jemalloc
// setting doesn't carry over to this bin. Needed here so --heap-breakdown's
// jemalloc_ctl stats.allocated reads reflect this process's real allocations.
#[global_allocator]
static ALLOC: tikv_jemallocator::Jemalloc = tikv_jemallocator::Jemalloc;

use alm_core::{Bar, Timeframe};
use alm_herald::registry::Registry;
use alm_ledger::{Ledger, LedgerConfig, LedgerObserver};
use tikv_jemalloc_ctl::{epoch, stats};
use tokio::sync::mpsc;

// Cheap real strategy — same EMA-cross shape already exercised in
// registry::tests — representative of a typical lightweight script.
//
// NOTE: this is a raw state check (`e5[0] > e20[0]`), not edge-triggered — it
// re-fires every bar the condition holds, not just on the crossing bar. Fine
// for eval-cost benchmarks (§01, --heap-breakdown), which don't care whether
// a signal is emitted — but it overstates real signal *volume* badly, which
// matters for --rx-latency. Use CROSS_SCRIPT there instead.
const SCRIPT: &str = "let e5 = ind.ema(5);\n\
let e20 = ind.ema(20);\n\
if e5[0] > e20[0] { long = true; }\n\
if e5[0] < e20[0] { exit = true; }";

// Edge-triggered variant — the idiomatic pattern real strategies use
// (see script-guide), fires once per crossing rather than continuously.
// For --rx-latency: a realistic signal-volume baseline instead of the
// worst-case rate SCRIPT produces.
const CROSS_SCRIPT: &str = "let e5 = ind.ema(5);\n\
let e20 = ind.ema(20);\n\
if cross_above(e5, e20) { long = true; }\n\
if cross_below(e5, e20) { exit = true; }";

// 15 indicator declarations + a longer condition — stand-in for a "someone
// actually wrote a complex strategy" script, to see whether AST-compile cost
// (found to be ~1% of per-hand memory with the 2-indicator SCRIPT above)
// stays negligible as script size grows, or whether it starts to matter.
const LONG_SCRIPT: &str = "let e5 = ind.ema(5);\n\
let e20 = ind.ema(20);\n\
let e50 = ind.ema(50);\n\
let s10 = ind.sma(10);\n\
let s30 = ind.sma(30);\n\
let r14 = ind.rsi(14);\n\
let c20 = ind.cci(20);\n\
let m14 = ind.mfi(14);\n\
let w15 = ind.wma(15);\n\
let h20 = ind.hma(20);\n\
let d12 = ind.dema(12);\n\
let t12 = ind.tema(12);\n\
let mom10 = ind.mom(10);\n\
let cmo14 = ind.cmo(14);\n\
let roc10 = ind.roc(10);\n\
if e5[0] > e20[0] && e20[0] > e50[0] && s10[0] > s30[0] && r14[0] < 70.0 && c20[0] < 100.0 && m14[0] < 80.0 { long = true; }\n\
if e5[0] < e20[0] || r14[0] > 70.0 || cmo14[0] > 50.0 { exit = true; }";

struct Args {
    hands: usize,
    symbols: usize,
    bars: usize,
    tf: Timeframe,
    flamegraph: bool,
    out: String,
    heap_breakdown: bool,
    rx_latency: bool,
    consumer_delay_us: u64,
    consumer_concurrency: usize,
    channel_capacity: usize,
}

fn parse_args() -> Args {
    let raw: Vec<String> = std::env::args().collect();
    let get = |flag: &str, default: &str| -> String {
        raw.windows(2)
            .find(|w| w[0] == flag)
            .map(|w| w[1].clone())
            .unwrap_or_else(|| default.to_string())
    };
    let tf = match get("--tf", "M1").as_str() {
        "M1" => Timeframe::M1,
        "M5" => Timeframe::M5,
        "M15" => Timeframe::M15,
        "M30" => Timeframe::M30,
        "H1" => Timeframe::H1,
        "H4" => Timeframe::H4,
        "D1" => Timeframe::D1,
        "W1" => Timeframe::W1,
        other => panic!("unknown --tf {other}"),
    };
    Args {
        hands: get("--hands", "1000").parse().expect("--hands must be a number"),
        symbols: get("--symbols", "1").parse().expect("--symbols must be a number"),
        bars: get("--bars", "300").parse().expect("--bars must be a number"),
        tf,
        flamegraph: raw.iter().any(|a| a == "--flamegraph"),
        out: get("--out", "flamegraph.svg"),
        heap_breakdown: raw.iter().any(|a| a == "--heap-breakdown"),
        rx_latency: raw.iter().any(|a| a == "--rx-latency"),
        consumer_delay_us: get("--consumer-delay-us", "0").parse().expect("--consumer-delay-us must be a number"),
        consumer_concurrency: get("--consumer-concurrency", "1").parse().expect("--consumer-concurrency must be a number"),
        channel_capacity: get("--channel-capacity", "1024").parse().expect("--channel-capacity must be a number"),
    }
}

/// Build a fresh Ledger+Registry, register `hands` hands round-robin across
/// `symbols` symbols, then replay `bars` synthetic ticks. Returns one
/// duration per tick — the wall-clock cost of advancing every symbol once,
/// i.e. what production must fit inside one `tf` interval.
fn run_workload(hands: usize, symbols: usize, bars: usize, tf: Timeframe) -> (Vec<Duration>, (usize, usize)) {
    let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
    let (tx, mut rx) = mpsc::channel(1024); // matches production sig_tx capacity
    let reg = Arc::new(Registry::new(ledger.clone(), tf, tx));
    reg.set_freshness_gate_ms(i64::MAX); // don't care about staleness here, only raw eval cost
    ledger.subscribe(reg.clone() as Arc<dyn LedgerObserver>);

    let symbol_names: Vec<String> = (0..symbols.max(1)).map(|i| format!("STRESS{i}")).collect();

    for i in 0..hands {
        let sym = &symbol_names[i % symbol_names.len()];
        reg.register(
            format!("hand-{i}"),
            format!("helm-{}", i % symbol_names.len()),
            sym.clone(),
            String::new(),
            false,
            SCRIPT.to_string(),
            tf,
        )
        .expect("register");
    }

    let tf_ms = tf.duration_ms();
    let mut ts = 1_700_000_000_000i64 - (1_700_000_000_000i64 % tf_ms);
    let mut tick_durations = Vec::with_capacity(bars);

    for b in 0..bars {
        ts += tf_ms;
        let tick_start = Instant::now();
        for sym in &symbol_names {
            let price = 100.0 + ((b % 50) as f64) * 0.1;
            let bar = Bar::new(ts, sym, price, price + 0.5, price - 0.5, price, 1.0);
            ledger.advance(tf, bar).expect("advance");
        }
        tick_durations.push(tick_start.elapsed());
        while rx.try_recv().is_ok() {} // drain so the bounded channel never backs up on its own
    }

    let chan_stats = reg.signal_channel_stats();
    (tick_durations, chan_stats)
}

/// Measures real enqueue→dequeue latency on the signal channel — not just
/// `evaluate_and_publish`'s (non-blocking) `try_send`, but how long a signal
/// actually sits in the channel before a consumer pulls it out.
///
/// Runs a genuine second OS thread as the consumer (`rx.try_recv()` + a small
/// backoff) racing concurrently against the main thread's tick loop — mirrors
/// production, where registry evaluation and the signal-forwarding consumer
/// (`signal_publisher`, handler.rs) are separate tokio tasks. Correlates each
/// received `HandSignal` back to the tick that produced it via `bar_ts`
/// (== that tick's `close_ts`, which we control).
///
/// `consumer_delay_us`, if nonzero, sleeps that long after every receive
/// before looking for the next one — simulating `signal_publisher`'s real
/// per-message cost (`js.publish(..).await` then `ack_future.await`, fully
/// sequential, no batching — handler.rs:904). At 0 the consumer is
/// artificially instant and this only measures the channel's own overhead;
/// with a realistic delay it tells you whether the channel — bounded at
/// 1024, same as production — can actually absorb a burst before
/// `evaluate_and_publish`'s `try_send` starts dropping signals
/// (`herald_signals_dropped_total{reason="channel_full"}`, captured here via
/// a real `metrics` recorder, not inferred).
fn run_rx_latency(hands: usize, symbols: usize, bars: usize, tf: Timeframe, consumer_delay_us: u64, consumer_concurrency: usize, channel_capacity: usize) {
    let snapshotter = install_metrics_recorder_once();

    let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
    let (tx, rx) = mpsc::channel(channel_capacity); // production default is 1024; --channel-capacity to test bigger
    let reg = Arc::new(Registry::new(ledger.clone(), tf, tx));
    reg.set_freshness_gate_ms(i64::MAX);
    ledger.subscribe(reg.clone() as Arc<dyn LedgerObserver>);

    let symbol_names: Vec<String> = (0..symbols.max(1)).map(|i| format!("STRESS{i}")).collect();
    for i in 0..hands {
        let sym = &symbol_names[i % symbol_names.len()];
        reg.register(
            format!("hand-{i}"), format!("helm-{}", i % symbol_names.len()),
            sym.clone(), String::new(), false, CROSS_SCRIPT.to_string(), tf,
        ).expect("register");
    }

    // Consumer thread: exactly what herald's real signal_publisher does —
    // pull off rx as fast as possible, nothing else. Records arrival Instants.
    //
    // Polls try_recv() + a short backoff instead of blocking_recv(), because
    // Ledger and Registry hold strong Arc references to each other (Ledger's
    // observer list holds Arc<Registry>; every Handle holds Arc<Ledger> for
    // warm-up reads) — a cycle that never reaches a zero refcount from the
    // outside, by design, since production never tears this down (herald
    // runs until process exit). So the channel never actually closes here;
    // shutdown is a stop flag instead. The 20µs backoff adds a small, fixed
    // upper bound to measured latency when the channel is momentarily empty —
    // negligible next to real queueing delay under load.
    // Peak channel depth sampler — polls the exact gauge production exposes
    // (`signal_channel_stats`, backed by `signal_tx.capacity()`), independent
    // of the consumer thread, to see whether the bounded channel ever
    // actually approaches/hits its 1024 cap during this run.
    let stop_sampler = Arc::new(std::sync::atomic::AtomicBool::new(false));
    let stop_sampler_bg = stop_sampler.clone();
    let reg_for_sampler = reg.clone();
    let peak_depth = Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let peak_depth_bg = peak_depth.clone();
    let sampler = thread::spawn(move || {
        while !stop_sampler_bg.load(std::sync::atomic::Ordering::Relaxed) {
            let (depth, _cap) = reg_for_sampler.signal_channel_stats();
            peak_depth_bg.fetch_max(depth, std::sync::atomic::Ordering::Relaxed);
            thread::sleep(Duration::from_micros(50));
        }
    });

    // `consumer_concurrency` workers sharing one receiver behind a Mutex —
    // dequeue is serialized (just a try_recv, microseconds) but the simulated
    // NATS round-trip (the sleep) happens OUTSIDE the lock, so up to
    // `consumer_concurrency` of those sleeps run truly in parallel. This
    // mirrors the fire-and-forget design (rx.recv → spawn(ack-await),
    // Semaphore(N) bounding in-flight acks) without needing a tokio runtime
    // in this tool: N OS threads ≈ N in-flight async tasks for throughput
    // purposes. concurrency=1 reproduces today's sequential signal_publisher
    // exactly, for a clean before/after comparison.
    let rx = Arc::new(Mutex::new(rx));
    let received: Arc<Mutex<Vec<(i64, Instant)>>> = Arc::new(Mutex::new(Vec::new()));
    let stop = Arc::new(std::sync::atomic::AtomicBool::new(false));
    let workers: Vec<_> = (0..consumer_concurrency.max(1))
        .map(|_| {
            let rx = rx.clone();
            let received_bg = received.clone();
            let stop_bg = stop.clone();
            thread::spawn(move || loop {
                let next = rx.lock().unwrap().try_recv();
                match next {
                    Ok(sig) => {
                        received_bg.lock().unwrap().push((sig.bar_ts, Instant::now()));
                        // Simulates the NATS publish+ack round-trip — held
                        // OUTSIDE the rx lock, so concurrent workers overlap it.
                        if consumer_delay_us > 0 {
                            thread::sleep(Duration::from_micros(consumer_delay_us));
                        }
                    }
                    Err(mpsc::error::TryRecvError::Disconnected) => break,
                    Err(mpsc::error::TryRecvError::Empty) => {
                        if stop_bg.load(std::sync::atomic::Ordering::Relaxed) {
                            loop {
                                let next = rx.lock().unwrap().try_recv();
                                match next {
                                    Ok(sig) => {
                                        received_bg.lock().unwrap().push((sig.bar_ts, Instant::now()));
                                        if consumer_delay_us > 0 {
                                            thread::sleep(Duration::from_micros(consumer_delay_us));
                                        }
                                    }
                                    Err(_) => break,
                                }
                            }
                            break;
                        }
                        thread::sleep(Duration::from_micros(20));
                    }
                }
            })
        })
        .collect();

    let tf_ms = tf.duration_ms();
    let mut ts = 1_700_000_000_000i64 - (1_700_000_000_000i64 % tf_ms);
    let mut tick_end_by_close_ts: HashMap<i64, Instant> = HashMap::with_capacity(bars);
    let workload_start = Instant::now();

    for b in 0..bars {
        ts += tf_ms;
        for sym in &symbol_names {
            let price = 100.0 + ((b % 50) as f64) * 0.1;
            let bar = Bar::new(ts, sym, price, price + 0.5, price - 0.5, price, 1.0);
            ledger.advance(tf, bar).expect("advance");
        }
        // close_ts must match evaluate_and_publish's own computation exactly
        // (HandSignal.bar_ts = outcome.ts + tf.duration_ms()) so we can
        // correlate a received signal back to the tick that produced it.
        tick_end_by_close_ts.insert(ts + tf_ms, Instant::now());
    }
    let production_elapsed = workload_start.elapsed();

    // Give the consumer a brief grace period before signalling it to stop —
    // it only actually exits once it sees the channel Empty AND this flag,
    // so a slow consumer with a large backlog still drains fully regardless
    // of how short this sleep is; consumer.join() below blocks until then.
    thread::sleep(Duration::from_millis(50));
    stop.store(true, std::sync::atomic::Ordering::Relaxed);
    let drain_start = Instant::now();
    for w in workers {
        w.join().expect("consumer worker thread panicked");
    }
    let drain_elapsed = drain_start.elapsed();
    stop_sampler.store(true, std::sync::atomic::Ordering::Relaxed);
    sampler.join().expect("sampler thread panicked");

    let received = received.lock().unwrap();
    let mut latencies: Vec<Duration> = received
        .iter()
        .filter_map(|(bar_ts, recv_at)| {
            tick_end_by_close_ts.get(bar_ts).map(|tick_end| recv_at.saturating_duration_since(*tick_end))
        })
        .collect();

    let dropped = signals_dropped_total(&snapshotter);
    let observed = latencies.len();
    let total_wall = production_elapsed + drain_elapsed; // production done, then drained to empty
    let throughput = observed as f64 / total_wall.as_secs_f64();
    let peak = peak_depth.load(std::sync::atomic::Ordering::Relaxed);

    println!(
        "\n--- rx receive latency ({hands} hands, {symbols} symbols, {bars} bars, consumer_delay={consumer_delay_us}µs, consumer_concurrency={consumer_concurrency}) ---"
    );
    println!(
        "signals observed: {observed}  dropped (channel full): {dropped}  peak channel depth: {peak}/{channel_capacity}"
    );
    println!(
        "throughput: {throughput:.0} signals/sec  (production {:.3}s + drain {:.3}s)",
        production_elapsed.as_secs_f64(), drain_elapsed.as_secs_f64(),
    );
    if latencies.is_empty() {
        println!("no signals observed — nothing to measure (try more --bars or a different price pattern)");
        return;
    }
    latencies.sort();
    let p50 = percentile(&latencies, 0.50);
    let p99 = percentile(&latencies, 0.99);
    let max = *latencies.last().unwrap();
    println!(
        "enqueue → rx.recv() latency  p50={:.3}ms  p99={:.3}ms  max={:.3}ms",
        p50.as_secs_f64() * 1000.0,
        p99.as_secs_f64() * 1000.0,
        max.as_secs_f64() * 1000.0,
    );
}

/// Installs a `metrics` `DebuggingRecorder` as the process-wide recorder so
/// `herald_signals_dropped_total` (real production metric, registry/mod.rs)
/// can be read back directly instead of inferred. Safe to call at most once
/// per process — `main` only reaches this from `--rx-latency`.
fn install_metrics_recorder_once() -> metrics_util::debugging::Snapshotter {
    let recorder = metrics_util::debugging::DebuggingRecorder::new();
    let snapshotter = recorder.snapshotter();
    recorder.install().expect("install metrics recorder");
    snapshotter
}

fn signals_dropped_total(snapshotter: &metrics_util::debugging::Snapshotter) -> u64 {
    snapshotter
        .snapshot()
        .into_vec()
        .into_iter()
        .filter(|(key, ..)| key.key().name() == "herald_signals_dropped_total")
        .map(|(_, _, _, value)| match value {
            metrics_util::debugging::DebugValue::Counter(v) => v,
            _ => 0,
        })
        .sum()
}

fn percentile(sorted: &[Duration], p: f64) -> Duration {
    if sorted.is_empty() {
        return Duration::ZERO;
    }
    let idx = ((sorted.len() - 1) as f64 * p).round() as usize;
    sorted[idx]
}

fn report(hands: usize, symbols: usize, tf: Timeframe, mut ticks: Vec<Duration>, chan: (usize, usize)) {
    ticks.sort();
    let budget_ms = tf.duration_ms();
    let p50 = percentile(&ticks, 0.50);
    let p95 = percentile(&ticks, 0.95);
    let p99 = percentile(&ticks, 0.99);
    let max = *ticks.last().unwrap_or(&Duration::ZERO);

    println!("hands={hands} symbols={symbols} tf={tf} budget={budget_ms}ms");
    println!(
        "tick latency  p50={:.3}ms  p95={:.3}ms  p99={:.3}ms  max={:.3}ms",
        p50.as_secs_f64() * 1000.0,
        p95.as_secs_f64() * 1000.0,
        p99.as_secs_f64() * 1000.0,
        max.as_secs_f64() * 1000.0,
    );
    println!("signal channel depth: {}/{}", chan.0, chan.1);

    let max_ms = max.as_secs_f64() * 1000.0;
    if max_ms > budget_ms as f64 {
        println!(
            "THRESHOLD BREACHED: worst tick ({max_ms:.1}ms) exceeds the {budget_ms}ms bar budget at {hands} hands / {symbols} symbols"
        );
    } else {
        println!(
            "OK: worst tick uses {:.2}% of the {budget_ms}ms bar budget",
            max_ms / budget_ms as f64 * 100.0
        );
    }
}

fn jemalloc_allocated() -> u64 {
    epoch::advance().expect("jemalloc epoch advance");
    stats::allocated::read().expect("jemalloc stats.allocated read") as u64
}

fn human_bytes_i(b: i64) -> String {
    let sign = if b < 0 { "-" } else { "" };
    let mut v = b.unsigned_abs() as f64;
    let units = ["B", "KiB", "MiB", "GiB"];
    let mut u = 0;
    while v >= 1024.0 && u < units.len() - 1 {
        v /= 1024.0;
        u += 1;
    }
    format!("{sign}{v:.2} {}", units[u])
}

/// Isolates where a hand's per-registration memory actually goes: building the
/// custom rhai::Engine (all packages + `ind`/`ta` functions) vs compiling the
/// script's AST against a shared engine vs the rest of `Handle::new`
/// (indicator VarBindings, HashMap entries, bar buffer, ...). Uses jemalloc's
/// own `stats.allocated` counter (herald's global allocator) rather than OS
/// RSS, so each step's delta is exact — no GC/page-return noise.
fn heap_breakdown(hands: usize, tf: Timeframe) {
    println!("\n--- heap breakdown ({hands} hands) ---");
    println!("(reference baselines below build UNSHARED engines on purpose, for comparison —");
    println!(" production now shares one Engine process-wide; see block C.)\n");

    // A0. N bare rhai::Engine::new() — Rhai's own stdlib, none of herald's additions.
    let before = jemalloc_allocated();
    let bare_engines: Vec<_> = (0..hands).map(|_| alm_strategy::script::build_bare_rhai_engine_for_bench()).collect();
    let after = jemalloc_allocated();
    let bare_total = after as i64 - before as i64;
    println!(
        "A0. {hands} bare rhai::Engine::new(), UNSHARED (Rhai's own stdlib): {:>12}  ({:.0} bytes/hand)",
        human_bytes_i(bare_total),
        bare_total as f64 / hands as f64
    );
    drop(bare_engines);

    // A. N fresh custom Engines only, UNSHARED — no script compiled yet.
    let before = jemalloc_allocated();
    let engines: Vec<_> = (0..hands).map(|_| alm_strategy::script::build_bench_engine()).collect();
    let after = jemalloc_allocated();
    let engine_total = after as i64 - before as i64;
    let herald_additions = engine_total - bare_total;
    println!(
        "A. {hands} bare custom Engines, UNSHARED (no AST):  {:>12}  ({:.0} bytes/hand)",
        human_bytes_i(engine_total),
        engine_total as f64 / hands as f64
    );
    println!(
        "   of which herald's own ind/ta/MEntry registrations: {:>12}  ({:.0} bytes/hand, {:.0}% of A)",
        human_bytes_i(herald_additions),
        herald_additions as f64 / hands as f64,
        herald_additions as f64 / engine_total as f64 * 100.0,
    );
    drop(engines);

    // A2. N clones of the REAL production shared engine — what registering an
    // extra hand actually pays today. Near-zero: cloning an Arc bumps a
    // refcount, nothing else.
    let before = jemalloc_allocated();
    let shared_clones: Vec<_> = (0..hands).map(|_| alm_strategy::script::shared_bench_engine()).collect();
    let after = jemalloc_allocated();
    let shared_clone_total = after as i64 - before as i64;
    println!(
        "A2. {hands} Arc clones of the shared production engine: {:>12}  ({:.1} bytes/hand)",
        human_bytes_i(shared_clone_total),
        shared_clone_total as f64 / hands as f64
    );
    drop(shared_clones);

    // B/C, repeated per script variant — does a longer, more-indicator-heavy
    // script change where the memory actually goes now that Engine is shared?
    let variants: [(&str, &str); 2] = [
        ("short (2 indicators)", SCRIPT),
        ("long (15 indicators)", LONG_SCRIPT),
    ];
    let mut totals_per_variant = Vec::new();
    for (label, script) in variants {
        println!("\n[{label}, {} bytes of script source]", script.len());

        // B. One (unshared, throwaway) Engine, N AST compiles against it —
        // isolates pure AST-compile cost regardless of what production shares.
        let throwaway_engine = alm_strategy::script::build_bench_engine();
        let before = jemalloc_allocated();
        let asts: Vec<_> = (0..hands).map(|_| throwaway_engine.compile(script).expect("compile")).collect();
        let after = jemalloc_allocated();
        let ast_total = after as i64 - before as i64;
        println!(
            "  B. {hands} AST compiles (1 throwaway Engine):   {:>12}  ({:.0} bytes/hand)",
            human_bytes_i(ast_total),
            ast_total as f64 / hands as f64
        );
        drop(asts);
        drop(throwaway_engine);

        // C. Full production path today: Registry::register, real shared Engine.
        // Note: the shared engine is built lazily on first-ever call — by this
        // point in the binary Phase 1 has already registered `hands` hands, so
        // that one-time ~330 KB is already paid and invisible here. That's
        // correct: it reflects real steady-state per-hand cost in a
        // long-running herald process, where the very first hand ever
        // registered pays it once and nobody else does.
        let ledger = Arc::new(Ledger::new(LedgerConfig::default()));
        let (tx, _rx) = mpsc::channel(1024);
        let reg = Registry::new(ledger.clone(), tf, tx);
        let before = jemalloc_allocated();
        for i in 0..hands {
            reg.register(
                format!("hand-{i}"), format!("helm-{i}"), "STRESS0".to_string(),
                String::new(), false, script.to_string(), tf,
            ).expect("register");
        }
        let after = jemalloc_allocated();
        let full_total = after as i64 - before as i64;
        println!(
            "  C. {hands} full Handle::new, SHARED Engine (production today): {:>12}  ({:.0} bytes/hand)",
            human_bytes_i(full_total),
            full_total as f64 / hands as f64
        );
        drop(reg);

        let rest = full_total - ast_total;
        println!(
            "  AST {:.0}% · rest (indicator bindings, HashMap, bar buf, ...) {:.0}% — no more Engine line item",
            ast_total as f64 / full_total as f64 * 100.0,
            rest as f64 / full_total as f64 * 100.0,
        );

        let unshared_projection = engine_total as f64 / hands as f64 + full_total as f64 / hands as f64;
        println!(
            "  vs. unshared projection (A + C): {:.0} bytes/hand → now {:.0} bytes/hand ({:.1}× smaller)",
            unshared_projection,
            full_total as f64 / hands as f64,
            unshared_projection / (full_total as f64 / hands as f64),
        );
        totals_per_variant.push((label, full_total as f64 / hands as f64));
    }

    if let [(sl, sf), (ll, lf)] = totals_per_variant[..] {
        println!(
            "\n{sl} → {ll}: total {:.1}× ({:.0} → {:.0} bytes/hand) — script length still barely matters",
            lf / sf, sf, lf,
        );
    }
}

fn main() {
    let args = parse_args();

    // ── Phase 1: timed run, no profiler attached ────────────────────────────
    let (ticks, chan_stats) = run_workload(args.hands, args.symbols, args.bars, args.tf);
    report(args.hands, args.symbols, args.tf, ticks, chan_stats);

    // ── Phase 1b: optional heap breakdown — Engine cost vs AST cost vs rest ─
    if args.heap_breakdown {
        heap_breakdown(args.hands, args.tf);
    }

    // ── Phase 1c: optional rx receive latency — fresh state, real 2nd thread ─
    if args.rx_latency {
        run_rx_latency(args.hands, args.symbols, args.bars, args.tf, args.consumer_delay_us, args.consumer_concurrency, args.channel_capacity);
    }

    // ── Phase 2: optional flamegraph, fresh state, profiler attached ───────
    if args.flamegraph {
        println!("\nprofiling replay loop for flamegraph (numbers above are NOT from this pass)...");
        let guard = pprof::ProfilerGuardBuilder::default()
            .frequency(997) // non-round Hz avoids lockstep sampling artifacts
            .blocklist(&["libc", "libgcc", "pthread", "vdso"])
            .build()
            .expect("profiler guard");

        let _ = run_workload(args.hands, args.symbols, args.bars, args.tf);

        match guard.report().build() {
            Ok(report) => {
                let file = std::fs::File::create(&args.out).expect("create flamegraph file");
                report.flamegraph(file).expect("write flamegraph");
                println!("flamegraph written to {}", args.out);
            }
            Err(e) => eprintln!("failed to build profiler report: {e}"),
        }
    }
}
