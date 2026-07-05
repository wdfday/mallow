/// Backtest benchmark — isolates I/O cost vs strategy compute cost.
///
/// Groups:
///   io_vs_compute   — full (load+run) vs compute_only (pre-loaded bars+run)
///                     difference = I/O overhead per backtest
///   strategy        — throughput across strategy complexity (cheap → expensive)
///                     all use SyncBus
///   batch_optimize  — parallel parameter sweep scalability
///
/// Data: BTCUSDT M1 testdata from alm-data crate (../data/testdata/).
///
/// Run all:
///   cargo bench -p alm-engine
///
/// Run one group:
///   cargo bench -p alm-engine -- io_vs_compute
use std::hint::black_box;
use std::path::{Path, PathBuf};

use criterion::{criterion_group, criterion_main, BenchmarkId, Criterion, Throughput};

use alm_core::Bar;
use alm_data::{BarFeed, BarVecFeed, ParquetFeed, RowGroupFeed};
use alm_engine::{run_batch, runner::BacktestJob, Engine,risk::PercentEquity};
use alm_strategy::{
    factory::build_strategy, ConnorsRsiStrategy, IchimokuCloud, MaCrossover,
    MacdCrossover,
};
use serde_json::json;

const SYMBOL: &str = "BTCUSDT";
const CAPITAL: f64 = 100_000.0;
const COMMISSION: f64 = 0.001;
const SLIPPAGE: f64 = 0.0005;
const RISK_FREE: f64 = 0.04;

// ── data helpers ──────────────────────────────────────────────────────────────

/// Bulk history file: `../data/testdata/BTCUSDT/M1/<first>.parquet`
/// (sorted → bulk file comes first by naming convention).
fn btc_m1_path() -> PathBuf {
    let dir = Path::new("../data/testdata/BTCUSDT/M1");
    let mut paths: Vec<PathBuf> = std::fs::read_dir(dir)
        .expect("testdata not found — run bench from almanac/crates/engine/")
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.extension().map_or(false, |x| x == "parquet"))
        .collect();
    paths.sort();
    paths.into_iter().next().expect("no parquet file in testdata/BTCUSDT/M1/")
}

/// Drain a feed into Vec<Bar> (needed for walk-forward and BarVecFeed source).
fn collect_bars(path: &Path, symbol: &str) -> Vec<Bar> {
    let mut f = ParquetFeed::load(path, symbol).expect("load bars");
    std::iter::from_fn(|| f.next()).collect()
}

fn make_engine() -> Engine<MaCrossover, PercentEquity> {
    Engine::sync(
        CAPITAL,
        MaCrossover::new(20, 50),
        PercentEquity::fractional(0.95, 1),
        COMMISSION,
        SLIPPAGE,
    )
}

// ── io_vs_compute ─────────────────────────────────────────────────────────────
//
// Answers: "how much time do I/O take vs strategy compute?"
//
// full:         ParquetFeed::load (I/O) + engine.run (compute)  [total cost]
// compute_only: BarVecFeed (RAM) + engine.run (compute)        [compute only]
// Δ = full - compute_only ≈ I/O cost

fn bench_io_vs_compute(c: &mut Criterion) {
    let path = btc_m1_path();
    let sym = SYMBOL;
    let bars = collect_bars(&path, sym);
    let n = bars.len() as u64;

    let mut group = c.benchmark_group("io_vs_compute");
    group.sample_size(10);
    group.throughput(Throughput::Elements(n));

    // Full: includes Parquet I/O + decompression + strategy compute
    group.bench_function("full/ParquetFeed", |b| {
        b.iter(|| {
            let mut f = ParquetFeed::load(&path, sym).unwrap();
            let mut eng = make_engine();
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    // Full: RowGroupFeed — lazy I/O interleaved with compute
    group.bench_function("full/RowGroupFeed", |b| {
        b.iter(|| {
            let mut f = RowGroupFeed::load(&path, sym).unwrap();
            let mut eng = make_engine();
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    // Compute only: data already in RAM, no I/O
    group.bench_function("compute_only", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let mut eng = make_engine();
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    group.finish();
}

// ── strategy: compute complexity ──────────────────────────────────────────────
//
// Answers: "how much does strategy complexity cost?"
// All use SyncBus + BarVecFeed (data already in RAM) → pure compute cost.
//
// Cheap:    MaCrossover(20,50)         — 2 SMAs, ~O(1) per bar
// Moderate: MacdCrossover(12,26,9)     — 3 EMAs + histogram
// Expensive: IchimokuCloud(9,26,52)    — 5 lines, 52-bar lookback
// Complex:  ConnorsRsiStrategy(3,2,100)— ConnorsRSI = RSI + streak-RSI + ROC-rank

fn bench_strategy(c: &mut Criterion) {
    let path = btc_m1_path();
    let sym = SYMBOL;
    let bars = collect_bars(&path, sym);
    let n = bars.len() as u64;

    let mut group = c.benchmark_group("strategy");
    group.sample_size(10);
    group.throughput(Throughput::Elements(n));

    group.bench_function("MaCrossover", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let mut eng = Engine::sync(CAPITAL, MaCrossover::new(20, 50),
                PercentEquity::fractional(0.95, 1), COMMISSION, SLIPPAGE);
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    group.bench_function("MacdCrossover", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let mut eng = Engine::sync(CAPITAL, MacdCrossover::new(12, 26, 9),
                PercentEquity::fractional(0.95, 1), COMMISSION, SLIPPAGE);
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    group.bench_function("IchimokuCloud", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let mut eng = Engine::sync(CAPITAL, IchimokuCloud::new(9, 26, 52),
                PercentEquity::fractional(0.95, 1), COMMISSION, SLIPPAGE);
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    group.bench_function("ConnorsRsi", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let mut eng = Engine::sync(CAPITAL, ConnorsRsiStrategy::new(3, 2, 100, 20.0, 80.0),
                PercentEquity::fractional(0.95, 1), COMMISSION, SLIPPAGE);
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    group.finish();
}

// ── strategy_expr: script vs named (expression overhead) ───────────────────────
//
// Answers: "how much does the script expression runtime add over a hardcoded struct?"
// Uses the same EMA-crossover logic in two forms:
//   named/MaCrossover  — hardcoded Rust struct (SMA-based, not EMA, but same O(1) cost)
//   script/EmaScript   — script interpreted per bar

fn bench_strategy_expr(c: &mut Criterion) {
    let path = btc_m1_path();
    let sym = SYMBOL;
    let bars = collect_bars(&path, sym);
    let n = bars.len() as u64;

    let mut group = c.benchmark_group("strategy_expr");
    group.sample_size(10);
    group.throughput(Throughput::Elements(n));

    // Baseline: named, typed (no expression runtime)
    group.bench_function("named/MaCrossover", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let mut eng = Engine::sync(
                CAPITAL,
                MaCrossover::new(20, 50),
                PercentEquity::fractional(0.95, 1),
                COMMISSION,
                SLIPPAGE,
            );
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    // Script: same logic via the script interpreter
    group.bench_function("rhai/EmaXover", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let strategy = build_strategy(
                "script",
                &json!({
                    "script": "\
                        let ema20 = ind.ema(20);\
                        let ema50 = ind.ema(50);\
                        let entry = cross_above(ema20, ema50);\
                        let exit  = cross_below(ema20, ema50);\
                    "
                }),
            )
            .unwrap();
            let mut eng = Engine::sync(
                CAPITAL,
                strategy,
                PercentEquity::fractional(0.95, 1),
                COMMISSION,
                SLIPPAGE,
            );
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    // Script: multi-indicator script
    group.bench_function("rhai/MultiIndicator", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let strategy = build_strategy(
                "script",
                &json!({
                    "script": "\
                        let rsi14 = ind.rsi(14);\
                        let ema20 = ind.ema(20);\
                        let ema50 = ind.ema(50);\
                        let macd  = ind.macd(12);\
                        let entry = rsi14[0] < 40.0 && ema20[0] > ema50[0] && macd[0] > 0.0;\
                        let exit  = rsi14[0] > 60.0 || ema20[0] < ema50[0];\
                    "
                }),
            )
            .unwrap();
            let mut eng = Engine::sync(
                CAPITAL,
                strategy,
                PercentEquity::fractional(0.95, 1),
                COMMISSION,
                SLIPPAGE,
            );
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    group.finish();
}

// ── batch_optimize: parallel parameter sweep ──────────────────────────────────

fn bench_batch(c: &mut Criterion) {
    let path = btc_m1_path();
    let sym = SYMBOL;
    let bars = collect_bars(&path, sym);

    let mut group = c.benchmark_group("batch_optimize");
    group.sample_size(10);

    for n_combos in [10usize, 50, 100] {
        group.bench_with_input(
            BenchmarkId::new("parallel", n_combos),
            &n_combos,
            |b, &n| {
                b.iter(|| {
                    let jobs: Vec<_> = (0..n)
                        .map(|i| BacktestJob {
                            id: format!("job_{i}"),
                            strategy: MaCrossover::new(10 + i % 20, 30 + i % 30),
                            risk: PercentEquity::fractional(0.95, 1),
                            initial_capital: CAPITAL,
                            commission_pct: COMMISSION,
                            slippage_pct: SLIPPAGE,
                            risk_free_annual: RISK_FREE,
                        })
                        .collect();
                    black_box(run_batch(jobs, bars.clone(), sym))
                })
            },
        );
    }
    group.finish();
}

// ── mtf_engines: heap-based vs pointer-sync ────────────────────────────────────

fn load_m1_h1_data() -> (Vec<Bar>, Vec<Bar>) {
    let path_m1 = btc_m1_path();
    let m1 = collect_bars(&path_m1, SYMBOL);
    
    let dir_h1 = Path::new("../data/testdata/BTCUSDT/H1");
    let mut paths_h1: Vec<PathBuf> = std::fs::read_dir(dir_h1)
        .expect("H1 testdata not found")
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.extension().map_or(false, |x| x == "parquet"))
        .collect();
    paths_h1.sort();
    let path_h1 = paths_h1.into_iter().next().expect("no H1 parquet file");
    let h1_all = collect_bars(&path_h1, SYMBOL);
    
    let t_start = m1.first().unwrap().timestamp;
    let t_end = m1.last().unwrap().timestamp;
    let h1: Vec<Bar> = h1_all
        .into_iter()
        .filter(|b| b.timestamp >= t_start && b.timestamp <= t_end)
        .collect();
    (m1, h1)
}

fn bench_mtf_engines(c: &mut Criterion) {
    let (m1_bars, h1_bars) = load_m1_h1_data();
    let n = m1_bars.len() as u64;

    let mut group = c.benchmark_group("mtf_engines");
    group.sample_size(10);
    group.throughput(Throughput::Elements(n));

    group.bench_function("heap_based", |b| {
        b.iter(|| {
            let m1_feed = BarVecFeed::new(m1_bars.clone(), SYMBOL.into());
            let h1_feed = BarVecFeed::new(h1_bars.clone(), SYMBOL.into());
            let mut eng = alm_engine::HeapMtfEngine::sync(
                CAPITAL,
                alm_strategy::MtfEmaRsiStrategy::new(),
                PercentEquity::fractional(0.95, 1),
                COMMISSION,
                SLIPPAGE,
            )
            .with_base_tf(alm_core::Timeframe::M1)
            .with_single_entry();
            eng.add_feed(alm_core::Timeframe::M1, m1_feed);
            eng.add_feed(alm_core::Timeframe::H1, h1_feed);
            black_box(eng.run(RISK_FREE))
        });
    });

    group.bench_function("pointer_sync", |b| {
        b.iter(|| {
            let m1_feed = BarVecFeed::new(m1_bars.clone(), SYMBOL.into());
            let h1_feed = BarVecFeed::new(h1_bars.clone(), SYMBOL.into());
            let mut eng = alm_engine::PointerSyncMtfEngine::sync(
                CAPITAL,
                alm_strategy::MtfEmaRsiStrategy::new(),
                PercentEquity::fractional(0.95, 1),
                COMMISSION,
                SLIPPAGE,
            )
            .with_base_tf(alm_core::Timeframe::M1)
            .with_single_entry();
            eng.add_feed(alm_core::Timeframe::M1, m1_feed);
            eng.add_feed(alm_core::Timeframe::H1, h1_feed);
            black_box(eng.run(RISK_FREE))
        });
    });

    group.finish();
}

// ── mtf_engines_full: full dataset comparison ──────────────────────────────────

fn binance_flat_dir(timeframe: &str) -> PathBuf {
    let relative = Path::new("../../../data/BinanceFlat").join(timeframe).join(SYMBOL);
    if relative.exists() {
        relative
    } else {
        Path::new("/Users/Giap/RustroverProjects/mallow/data/BinanceFlat").join(timeframe).join(SYMBOL)
    }
}

fn collect_all_bars(dir_path: &Path, symbol: &str) -> Vec<Bar> {
    let mut paths: Vec<PathBuf> = std::fs::read_dir(dir_path)
        .unwrap_or_else(|e| panic!("failed to read dir {}: {}", dir_path.display(), e))
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.extension().map_or(false, |x| x == "parquet"))
        .collect();
    paths.sort();
    
    let mut all_bars = Vec::new();
    for path in paths {
        if let Ok(mut feed) = ParquetFeed::load(&path, symbol) {
            while let Some(bar) = feed.next() {
                all_bars.push(bar);
            }
        }
    }
    all_bars
}

fn load_full_m1_h1_data() -> (Vec<Bar>, Vec<Bar>) {
    let dir_m1 = binance_flat_dir("M1");
    let m1 = collect_all_bars(&dir_m1, SYMBOL);
    
    let dir_h1 = binance_flat_dir("H1");
    let h1_all = collect_all_bars(&dir_h1, SYMBOL);
    
    if m1.is_empty() || h1_all.is_empty() {
        panic!("BinanceFlat data not found at {}", dir_m1.display());
    }

    let t_start = m1.first().unwrap().timestamp;
    let t_end = m1.last().unwrap().timestamp;
    let h1: Vec<Bar> = h1_all
        .into_iter()
        .filter(|b| b.timestamp >= t_start && b.timestamp <= t_end)
        .collect();
    (m1, h1)
}

fn bench_mtf_engines_full(c: &mut Criterion) {
    let (m1_bars, h1_bars) = load_full_m1_h1_data();
    let n = m1_bars.len() as u64;

    let mut group = c.benchmark_group("mtf_engines_full");
    group.sample_size(10);
    group.warm_up_time(std::time::Duration::from_secs(2));
    group.throughput(Throughput::Elements(n));

    group.bench_function("heap_based", |b| {
        b.iter(|| {
            let m1_feed = BarVecFeed::new(m1_bars.clone(), SYMBOL.into());
            let h1_feed = BarVecFeed::new(h1_bars.clone(), SYMBOL.into());
            let mut eng = alm_engine::HeapMtfEngine::sync(
                CAPITAL,
                alm_strategy::MtfEmaRsiStrategy::new(),
                PercentEquity::fractional(0.95, 1),
                COMMISSION,
                SLIPPAGE,
            )
            .with_base_tf(alm_core::Timeframe::M1)
            .with_single_entry();
            eng.add_feed(alm_core::Timeframe::M1, m1_feed);
            eng.add_feed(alm_core::Timeframe::H1, h1_feed);
            black_box(eng.run(RISK_FREE))
        });
    });

    group.bench_function("pointer_sync", |b| {
        b.iter(|| {
            let m1_feed = BarVecFeed::new(m1_bars.clone(), SYMBOL.into());
            let h1_feed = BarVecFeed::new(h1_bars.clone(), SYMBOL.into());
            let mut eng = alm_engine::PointerSyncMtfEngine::sync(
                CAPITAL,
                alm_strategy::MtfEmaRsiStrategy::new(),
                PercentEquity::fractional(0.95, 1),
                COMMISSION,
                SLIPPAGE,
            )
            .with_base_tf(alm_core::Timeframe::M1)
            .with_single_entry();
            eng.add_feed(alm_core::Timeframe::M1, m1_feed);
            eng.add_feed(alm_core::Timeframe::H1, h1_feed);
            black_box(eng.run(RISK_FREE))
        });
    });

    group.finish();
}

// ── entry point ───────────────────────────────────────────────────────────────

criterion_group!(benches, bench_io_vs_compute, bench_strategy, bench_strategy_expr, bench_batch, bench_mtf_engines, bench_mtf_engines_full);
criterion_main!(benches);
