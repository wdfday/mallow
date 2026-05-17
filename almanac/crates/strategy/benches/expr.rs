mod bench_utils;

/// Benchmark: ScriptStrategy vs hardcoded struct.
///
/// Tách rõ 2 phần:
/// - construct - thời gian tạo strategy (1 lần)
/// - run - thời gian xử lý 1000 bar (vòng lặp thực sự)
///
/// Chạy:
/// - cargo bench -p alm-strategy
/// - cargo bench -p alm-strategy -- rsi_run   (chỉ xem group run)

use alm_core::{bar::Bar, strategy::Strategy};
use alm_strategy::{
    factory::build_strategy,
    named::{rsi_mean_rev::RsiMeanRev, kitchen_sink::KitchenSinkStrategy},
};
use criterion::{criterion_group, criterion_main, BenchmarkId, Criterion};
use serde_json::json;
use std::hint::black_box;

// ── Synthetic data ────────────────────────────────────────────────────────────

use bench_utils::make_bars;

fn run_all(s: &mut dyn Strategy, bars: &[Bar]) -> usize {
    bars.iter().map(|b| s.on_bar(black_box(b)).len()).sum()
}

// ── RSI ───────────────────────────────────────────────────────────────────────

fn bench_rsi_construct(c: &mut Criterion) {
    let mut group = c.benchmark_group("rsi_construct");

    group.bench_function("hardcoded", |b| b.iter(|| {
        RsiMeanRev::new(14, 35.0, 65.0)
    }));

    group.bench_function("script", |b| b.iter(|| {
        build_strategy("script", &json!({
            "script": "\
                let rsi14 = ind.rsi(14);\
                if rsi14[0] < 35.0 { entry = true; }\
                if rsi14[0] > 65.0 { exit  = true; }\
            "
        })).unwrap()
    }));

    group.finish();
}

fn bench_rsi_run(c: &mut Criterion) {
    let bars = make_bars(1_000);
    let mut group = c.benchmark_group("rsi_run/1000bars");

    group.bench_with_input(BenchmarkId::from_parameter("hardcoded"), &bars, |b, bars| {
        let mut s = RsiMeanRev::new(14, 35.0, 65.0);
        b.iter(|| { s.reset(); run_all(&mut s, bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("script"), &bars, |b, bars| {
        let mut s = build_strategy("script", &json!({
            "script": "\
                let rsi14 = ind.rsi(14);\
                if rsi14[0] < 35.0 { entry = true; }\
                if rsi14[0] > 65.0 { exit  = true; }\
            "
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.finish();
}

// ── EMA crossover ─────────────────────────────────────────────────────────────

fn bench_ema_run(c: &mut Criterion) {
    let bars = make_bars(1_000);
    let mut group = c.benchmark_group("ema_cross_run/1000bars");

    group.bench_with_input(BenchmarkId::from_parameter("script"), &bars, |b, bars| {
        let mut s = build_strategy("script", &json!({
            "script": "\
                let ema20 = ind.ema(20);\
                let ema50 = ind.ema(50);\
                if cross_above(ema20, ema50) { entry = true; }\
                if cross_below(ema20, ema50) { exit  = true; }\
            "
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.finish();
}

// ── Multi-indicator ───────────────────────────────────────────────────────────

fn bench_multi_run(c: &mut Criterion) {
    let bars = make_bars(1_000);
    let mut group = c.benchmark_group("multi_run/1000bars");

    group.bench_with_input(BenchmarkId::from_parameter("script"), &bars, |b, bars| {
        let mut s = build_strategy("script", &json!({
            "script": "\
                let rsi14 = ind.rsi(14);\
                let ema20 = ind.ema(20);\
                let ema50 = ind.ema(50);\
                let macd  = ind.macd(12);\
                if rsi14[0] < 40.0 && ema20[0] > ema50[0] && macd[0] > 0.0 { entry = true; }\
                if rsi14[0] > 60.0 || ema20[0] < ema50[0] { exit = true; }\
            "
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.finish();
}

// ── Kitchen Sink ──────────────────────────────────────────────────────────────
//
// Compares hardcoded vs script on a compute-heavy 10-indicator + MTF strategy:
//   hardcoded  — pure Rust struct, zero interpreter overhead
//   script     — 100% faithful to the spec script, runs the full script engine

fn bench_kitchen_sink_run(c: &mut Criterion) {
    let bars = make_bars(1_000);
    let mut group = c.benchmark_group("kitchen_sink_run/1000bars");

    // Use the canonical RHAI_SCRIPT exposed by KitchenSinkStrategy so both
    // implementations execute the exact same logic — otherwise this comparison
    // measures different strategies instead of interpreter overhead.
    let script = KitchenSinkStrategy::new()
        .script()
        .expect("KitchenSinkStrategy::script() must return canonical RHAI_SCRIPT");

    group.bench_with_input(BenchmarkId::from_parameter("hardcoded"), &bars, |b, bars| {
        let mut s = KitchenSinkStrategy::new();
        b.iter(|| { s.reset(); run_all(&mut s, bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("script_full"), &bars, |b, bars| {
        let mut s = build_strategy("script", &json!({ "script": script })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.finish();
}

// ── BTC M1 real data ────────────────────────────────���────────────────────────
//
// Same kitchen-sink strategy on ~2M real BTC M1 bars (2022-04 → 2026-04).
// Measures wall-clock throughput on realistic market data.

fn bench_btc_m1_real(c: &mut Criterion) {
    let bars = bench_utils::load_btc_m1_bars();
    if bars.is_empty() {
        eprintln!("btc_m1_real: parquet not found, skipping");
        return;
    }
    eprintln!("btc_m1_real: loaded {} bars", bars.len());

    let mut group = c.benchmark_group("btc_m1_real");
    group.sample_size(10);

    let script = KitchenSinkStrategy::new()
        .script()
        .expect("KitchenSinkStrategy::script() must return canonical RHAI_SCRIPT");

    group.bench_with_input(BenchmarkId::from_parameter("hardcoded"), &bars, |b, bars| {
        let mut s = KitchenSinkStrategy::new();
        b.iter(|| { s.reset(); run_all(&mut s, bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("script_full"), &bars, |b, bars| {
        let mut s = build_strategy("script", &json!({ "script": script })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.finish();
}

criterion_group!(benches, bench_rsi_construct, bench_rsi_run, bench_ema_run, bench_multi_run, bench_kitchen_sink_run, bench_btc_m1_real);
criterion_main!(benches);
