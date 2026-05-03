/// Feed benchmark — real BTCUSDT Parquet data.
///
/// Groups:
///   load/{tf}       — construction only (I/O + decompression + buffer build)
///   drain/{tf}      — iteration only (feed pre-loaded, just next() loop)
///   full_pass/{tf}  — load + drain end-to-end (real single-pass backtest)
///
/// Run all:
///   cargo bench -p alm-data
///
/// Run one group:
///   cargo bench -p alm-data -- drain/M1
///
/// Flamegraph:
///   FEED_BENCH_PROFILE=1 RUSTFLAGS="-C force-frame-pointers=yes" cargo bench -p alm-data
use std::hint::black_box;
use std::path::{Path, PathBuf};

use criterion::{criterion_group, BatchSize, BenchmarkId, Criterion, Throughput};
use pprof::criterion::{Output, PProfProfiler};

use alm_data::{BarFeed, BarVecFeed, ParquetFeed, RowGroupFeed};

const SYMBOL: &str = "BTCUSDT";

// ── data helpers ──────────────────────────────────────────────────────────────

fn tf_paths(tf: &str) -> Vec<PathBuf> {
    let dir = Path::new("testdata/BTCUSDT").join(tf);
    let mut paths: Vec<PathBuf> = std::fs::read_dir(&dir)
        .unwrap_or_else(|_| panic!("missing {}", dir.display()))
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.extension().map_or(false, |x| x == "parquet"))
        .collect();
    paths.sort();
    paths
}

fn bulk_file(tf: &str) -> PathBuf {
    tf_paths(tf).into_iter().next().unwrap_or_else(|| panic!("no file for {tf}"))
}

fn n_bars(tf: &str) -> u64 {
    ParquetFeed::load(&bulk_file(tf), SYMBOL).unwrap().len() as u64
}

/// Drain a ParquetFeed into Vec<Bar> — used to set up BarVecFeed.
fn load_bars(tf: &str) -> Vec<alm_core::Bar> {
    let mut f = ParquetFeed::load(&bulk_file(tf), SYMBOL).unwrap();
    std::iter::from_fn(|| f.next()).collect()
}

// ── RSS measurement ───────────────────────────────────────────────────────────

fn rss_mib() -> f64 {
    #[cfg(target_os = "linux")]
    return std::fs::read_to_string("/proc/self/status").ok()
        .and_then(|s| s.lines().find(|l| l.starts_with("VmRSS:"))
            .and_then(|l| l.split_whitespace().nth(1))
            .and_then(|n| n.parse::<f64>().ok()))
        .map(|kb| kb / 1024.0).unwrap_or(0.0);

    #[cfg(target_os = "macos")]
    {
        let pid = std::process::id().to_string();
        std::process::Command::new("ps")
            .args(["-o", "rss=", "-p", &pid])
            .output().ok()
            .and_then(|o| String::from_utf8(o.stdout).ok())
            .and_then(|s| s.trim().parse::<f64>().ok())
            .map(|kb| kb / 1024.0).unwrap_or(0.0)
    }

    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    0.0
}

fn ram_row(label: &str, tf: &str, kind: &str, make: impl FnOnce() -> Box<dyn BarFeed>) {
    let before = rss_mib();
    let f = make();
    let after_load = rss_mib();
    drop(f);
    let after_drop = rss_mib();
    eprintln!("║  {label:<14} {kind:<8} {tf:<4}  load: {:+6.1} MiB  drop: {:+6.1} MiB  ║",
        after_load - before, after_drop - after_load);
}

fn ram_report() {
    eprintln!("\n╔══════════════════════════════════════════════════════════════╗");
    eprintln!("║                  RAM report — RSS delta                    ║");
    eprintln!("╠══════════════════════════════════════════════════════════════╣");
    for tf in ["M1", "H1", "D1"] {
        let path = bulk_file(tf);
        let paths = tf_paths(tf);
        let refs: Vec<&Path> = paths.iter().map(|p| p.as_path()).collect();
        ram_row("ParquetFeed",  tf, "single", || Box::new(ParquetFeed::load(&path, SYMBOL).unwrap()));
        ram_row("RowGroupFeed", tf, "single", || Box::new(RowGroupFeed::load(&path, SYMBOL).unwrap()));
        ram_row("ParquetFeed",  tf, "many",   || Box::new(ParquetFeed::load_many(&refs, SYMBOL).unwrap()));
        ram_row("RowGroupFeed", tf, "many",   || Box::new(RowGroupFeed::load_many(&refs, SYMBOL).unwrap()));
        eprintln!("╠══════════════════════════════════════════════════════════════╣");
    }
    eprintln!("╚══════════════════════════════════════════════════════════════╝\n");
}

// ── load: construction time only ──────────────────────────────────────────────
//
// Measures: open file, read metadata, decompress first row-group (RowGroupFeed)
//           OR decompress all row-groups (ParquetFeed).

fn bench_load(c: &mut Criterion) {
    let mut group = c.benchmark_group("load");
    group.sample_size(10);

    for tf in ["M1", "H1", "D1"] {
        let path = bulk_file(tf);
        let paths = tf_paths(tf);
        let refs: Vec<&Path> = paths.iter().map(|p| p.as_path()).collect();

        // ParquetFeed: reads and decompresses ALL row-groups into Vecs
        group.bench_with_input(BenchmarkId::new("ParquetFeed/single", tf), tf, |b, _| {
            b.iter(|| black_box(ParquetFeed::load(&path, SYMBOL).unwrap()));
        });
        group.bench_with_input(BenchmarkId::new("ParquetFeed/many", tf), tf, |b, _| {
            b.iter(|| black_box(ParquetFeed::load_many(&refs, SYMBOL).unwrap()));
        });

        // RowGroupFeed: reads metadata + decompresses first row-group only
        group.bench_with_input(BenchmarkId::new("RowGroupFeed/single", tf), tf, |b, _| {
            b.iter(|| black_box(RowGroupFeed::load(&path, SYMBOL).unwrap()));
        });
        group.bench_with_input(BenchmarkId::new("RowGroupFeed/many", tf), tf, |b, _| {
            b.iter(|| black_box(RowGroupFeed::load_many(&refs, SYMBOL).unwrap()));
        });

        // BarVecFeed: cost = ParquetFeed load + collect into Vec<Bar> + detect timeframe
        // (baseline: what walk-forward pays before any iteration)
        group.bench_with_input(BenchmarkId::new("BarVecFeed", tf), tf, |b, _| {
            b.iter(|| {
                let mut f = ParquetFeed::load(&path, SYMBOL).unwrap();
                let bars: Vec<_> = std::iter::from_fn(|| f.next()).collect();
                black_box(BarVecFeed::new(bars, SYMBOL.to_string()))
            });
        });
    }
    group.finish();
}

// ── drain: iteration only (I/O excluded) ─────────────────────────────────────
//
// iter_batched setup (not timed) creates the feed.
// Routine (timed) drains all bars via next().
// Difference = cache layout: BarVecFeed is row (Vec<Bar>), ParquetFeed is column (6×Vec<f64>).

fn bench_drain(c: &mut Criterion) {
    let mut group = c.benchmark_group("drain");
    group.sample_size(10);

    for tf in ["M1", "H1", "D1"] {
        let path = bulk_file(tf);
        let bars = load_bars(tf);
        group.throughput(Throughput::Elements(n_bars(tf)));

        group.bench_with_input(BenchmarkId::new("ParquetFeed", tf), tf, |b, _| {
            b.iter_batched(
                || ParquetFeed::load(&path, SYMBOL).unwrap(),
                |mut f| while let Some(bar) = f.next() { black_box(bar); },
                BatchSize::LargeInput,
            );
        });

        group.bench_with_input(BenchmarkId::new("RowGroupFeed", tf), tf, |b, _| {
            b.iter_batched(
                || RowGroupFeed::load(&path, SYMBOL).unwrap(),
                |mut f| while let Some(bar) = f.next() { black_box(bar); },
                BatchSize::LargeInput,
            );
        });

        // BarVecFeed: setup clones Vec<Bar> (not timed), drain = pure iteration baseline
        group.bench_with_input(BenchmarkId::new("BarVecFeed", tf), tf, |b, _| {
            b.iter_batched(
                || BarVecFeed::new(bars.clone(), SYMBOL.to_string()),
                |mut f| while let Some(bar) = f.next() { black_box(bar); },
                BatchSize::LargeInput,
            );
        });
    }
    group.finish();
}

// ── full_pass: load + drain (real single-pass backtest cost) ──────────────────
//
// This is what a backtest actually does: open feed, drain all bars, done.
// RowGroupFeed decompresses lazily per row-group — no upfront RAM spike.
// ParquetFeed decompresses upfront — higher RAM but potentially warmer cache.

fn bench_full_pass(c: &mut Criterion) {
    let mut group = c.benchmark_group("full_pass");
    group.sample_size(10);

    for tf in ["M1", "H1", "D1"] {
        let path = bulk_file(tf);
        let paths = tf_paths(tf);
        let refs: Vec<&Path> = paths.iter().map(|p| p.as_path()).collect();
        group.throughput(Throughput::Elements(n_bars(tf)));

        group.bench_with_input(BenchmarkId::new("ParquetFeed/single", tf), tf, |b, _| {
            b.iter_batched(
                || (),
                |_| {
                    let mut f = ParquetFeed::load(&path, SYMBOL).unwrap();
                    while let Some(bar) = f.next() { black_box(bar); }
                },
                BatchSize::LargeInput,
            );
        });

        group.bench_with_input(BenchmarkId::new("ParquetFeed/many", tf), tf, |b, _| {
            b.iter_batched(
                || (),
                |_| {
                    let mut f = ParquetFeed::load_many(&refs, SYMBOL).unwrap();
                    while let Some(bar) = f.next() { black_box(bar); }
                },
                BatchSize::LargeInput,
            );
        });

        group.bench_with_input(BenchmarkId::new("RowGroupFeed/single", tf), tf, |b, _| {
            b.iter_batched(
                || (),
                |_| {
                    let mut f = RowGroupFeed::load(&path, SYMBOL).unwrap();
                    while let Some(bar) = f.next() { black_box(bar); }
                },
                BatchSize::LargeInput,
            );
        });

        group.bench_with_input(BenchmarkId::new("RowGroupFeed/many", tf), tf, |b, _| {
            b.iter_batched(
                || (),
                |_| {
                    let mut f = RowGroupFeed::load_many(&refs, SYMBOL).unwrap();
                    while let Some(bar) = f.next() { black_box(bar); }
                },
                BatchSize::LargeInput,
            );
        });

        // BarVecFeed: load = ParquetFeed load + collect, drain = iterate Vec<Bar>
        let bars = load_bars(tf);
        group.bench_with_input(BenchmarkId::new("BarVecFeed", tf), tf, |b, _| {
            b.iter_batched(
                || bars.clone(),
                |b| {
                    let mut f = BarVecFeed::new(b, SYMBOL.to_string());
                    while let Some(bar) = f.next() { black_box(bar); }
                },
                BatchSize::LargeInput,
            );
        });
    }
    group.finish();
}

// ── entry point ───────────────────────────────────────────────────────────────

criterion_group!(
    name    = benches_timing;
    config  = Criterion::default();
    targets = bench_load, bench_drain, bench_full_pass
);

criterion_group!(
    name    = benches_flamegraph;
    config  = Criterion::default()
                .with_profiler(PProfProfiler::new(1000, Output::Flamegraph(None)));
    targets = bench_load, bench_drain, bench_full_pass
);

fn main() {
    ram_report();

    let profiling = std::env::var("FEED_BENCH_PROFILE").map_or(false, |v| v == "1");

    let mut c = if profiling {
        eprintln!("Flamegraph mode — SVGs → target/criterion/*/profile/flamegraph.svg");
        Criterion::default()
            .configure_from_args()
            .with_profiler(PProfProfiler::new(1000, Output::Flamegraph(None)))
    } else {
        Criterion::default().configure_from_args()
    };

    bench_load(&mut c);
    bench_drain(&mut c);
    bench_full_pass(&mut c);
    c.final_summary();
}
