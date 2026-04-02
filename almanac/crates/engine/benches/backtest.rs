use alm_data::{BarFeed, InMemoryFeed, ParquetFeed};
use alm_engine::{run_batch, runner::BacktestJob, Engine};
use alm_strategy::{FixedFractional, MaCrossover};
use criterion::{criterion_group, criterion_main, BenchmarkId, Criterion};
use std::path::Path;

fn load_bars(symbol: &str) -> Vec<alm_core::Bar> {
    let base =
        std::env::var("DATA_DIR").unwrap_or_else(|_| "../../us-data/data/Polygon/stocks".into());
    let dir = Path::new(&base).join(symbol);

    let path = std::fs::read_dir(&dir)
        .expect("data dir not found — set DATA_DIR env var")
        .filter_map(|e| e.ok())
        .find(|e| e.path().extension().and_then(|x| x.to_str()) == Some("parquet"))
        .expect("no parquet file found")
        .path();

    let mut feed = ParquetFeed::load(&path, symbol).expect("load parquet");

    let mut bars = Vec::new();
    while let Some(bar) = feed.next() {
        bars.push(bar);
    }
    bars
}

fn bench_single(c: &mut Criterion) {
    let bars = load_bars("AAPL");
    let n = bars.len();

    c.bench_function("ma_crossover_sync", |b| {
        b.iter(|| {
            let strategy = MaCrossover::new(20, 50);
            let risk = FixedFractional::new(0.02, 5);
            let mut feed = InMemoryFeed::new(bars.clone(), "AAPL".into());
            let mut engine = Engine::sync(100_000.0, strategy, risk, 0.001, 0.0005);
            engine.run(&mut feed, 0.04)
        })
    });

    c.bench_function("ma_crossover_tokio", |b| {
        b.iter(|| {
            let strategy = MaCrossover::new(20, 50);
            let risk = FixedFractional::new(0.02, 5);
            let mut feed = InMemoryFeed::new(bars.clone(), "AAPL".into());
            let mut engine = Engine::tokio(100_000.0, strategy, risk, 0.001, 0.0005);
            engine.run(&mut feed, 0.04)
        })
    });

    // Report bars/sec
    println!("AAPL bars: {n}");
}

fn bench_batch(c: &mut Criterion) {
    let bars = load_bars("AAPL");
    let mut group = c.benchmark_group("batch_optimize");

    for n_combos in [10usize, 50, 100] {
        group.bench_with_input(
            BenchmarkId::new("ma_crossover_parallel", n_combos),
            &n_combos,
            |b, &n| {
                b.iter(|| {
                    let jobs: Vec<_> = (0..n)
                        .map(|i| BacktestJob {
                            id: format!("job_{i}"),
                            strategy: MaCrossover::new(10 + i % 20, 30 + i % 30),
                            risk: FixedFractional::new(0.02, 5),
                            initial_capital: 100_000.0,
                            commission_pct: 0.001,
                            slippage_pct: 0.0005,
                            risk_free_annual: 0.04,
                        })
                        .collect();
                    run_batch(jobs, bars.clone(), "AAPL")
                })
            },
        );
    }

    group.finish();
}

criterion_group!(benches, bench_single, bench_batch);
criterion_main!(benches);
