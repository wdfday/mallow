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
use alm_engine::{run_batch, runner::BacktestJob, Engine,risk::FixedFractional};
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

fn make_engine() -> Engine<MaCrossover, FixedFractional> {
    Engine::sync(
        CAPITAL,
        MaCrossover::new(20, 50),
        FixedFractional::fractional(0.95, 1),
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
                FixedFractional::fractional(0.95, 1), COMMISSION, SLIPPAGE);
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    group.bench_function("MacdCrossover", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let mut eng = Engine::sync(CAPITAL, MacdCrossover::new(12, 26, 9),
                FixedFractional::fractional(0.95, 1), COMMISSION, SLIPPAGE);
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    group.bench_function("IchimokuCloud", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let mut eng = Engine::sync(CAPITAL, IchimokuCloud::new(9, 26, 52),
                FixedFractional::fractional(0.95, 1), COMMISSION, SLIPPAGE);
            black_box(eng.run(&mut f, RISK_FREE))
        });
    });

    group.bench_function("ConnorsRsi", |b| {
        b.iter(|| {
            let mut f = BarVecFeed::new(bars.clone(), sym.into());
            let mut eng = Engine::sync(CAPITAL, ConnorsRsiStrategy::new(3, 2, 100, 20.0, 80.0),
                FixedFractional::fractional(0.95, 1), COMMISSION, SLIPPAGE);
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
                FixedFractional::fractional(0.95, 1),
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
                FixedFractional::fractional(0.95, 1),
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
                FixedFractional::fractional(0.95, 1),
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
                            risk: FixedFractional::fractional(0.95, 1),
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

// ── entry point ───────────────────────────────────────────────────────────────

criterion_group!(benches, bench_io_vs_compute, bench_strategy, bench_strategy_expr, bench_batch);
criterion_main!(benches);
