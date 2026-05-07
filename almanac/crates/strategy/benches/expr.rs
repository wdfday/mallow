mod bench_utils;

/// Benchmark: RhaiStrategy vs hardcoded struct.
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
use criterion::{black_box, criterion_group, criterion_main, BenchmarkId, Criterion};
use serde_json::json;

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

    group.bench_function("rhai", |b| b.iter(|| {
        build_strategy("rhai", &json!({
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

    group.bench_with_input(BenchmarkId::from_parameter("rhai"), &bars, |b, bars| {
        let mut s = build_strategy("rhai", &json!({
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

    group.bench_with_input(BenchmarkId::from_parameter("rhai"), &bars, |b, bars| {
        let mut s = build_strategy("rhai", &json!({
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

    group.bench_with_input(BenchmarkId::from_parameter("rhai"), &bars, |b, bars| {
        let mut s = build_strategy("rhai", &json!({
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
// Compares hardcoded vs Rhai on a compute-heavy 10-indicator + MTF strategy:
//   hardcoded  — pure Rust struct, zero interpreter overhead
//   rhai       — 100% faithful to the spec script, runs full Rhai VM

fn bench_kitchen_sink_run(c: &mut Criterion) {
    let bars = make_bars(1_000);
    let mut group = c.benchmark_group("kitchen_sink_run/1000bars");

    group.bench_with_input(BenchmarkId::from_parameter("hardcoded"), &bars, |b, bars| {
        let mut s = KitchenSinkStrategy::new();
        b.iter(|| { s.reset(); run_all(&mut s, bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("rhai_full"), &bars, |b, bars| {
        let mut s = build_strategy("rhai", &json!({ "script":
r#"let ema9   = ind.ema(9,    4);
let ema21  = ind.ema(21,   4);
let ema50  = ind.ema(50,   4);
let rsi14  = ind.rsi(14,   4);
let adx14  = ind.adx(14,   5);
let atr14  = ind.atr(14,   3);
let macd   = ind.macd(12,  3);
let bb_u   = ind.bb_upper(20, 3);
let bb_l   = ind.bb_lower(20, 3);
let h1_ema = ind.ema(20, "H1", 3);

let trend   = adx14[0] > 25.0 && rising_n(adx14, 3);
let mom     = momentum(rsi14, 3) > 0.0;
let squeeze = (bb_u[0] - bb_l[0]) < atr14[0] * 1.5;
let h_break = highest(close, 20) == close[0];

if cross_above(ema9, ema21) && above(ema21, ema50)
   && rsi14[0] > 50.0 && rsi14[0] < 70.0
   && trend && mom && squeeze && h_break
   && above(h1_ema, ema50) {
    entry = true;
    tp    = close[0] + atr14[0] * 2.5;
    sl    = close[0] - atr14[0] * 1.5;
}
if cross_below(ema9, ema21) || rsi14[0] > 80.0 || falling_n(adx14, 2) {
    exit = true;
}"#
        })).unwrap();
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

    group.bench_with_input(BenchmarkId::from_parameter("hardcoded"), &bars, |b, bars| {
        let mut s = KitchenSinkStrategy::new();
        b.iter(|| { s.reset(); run_all(&mut s, bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("rhai_full"), &bars, |b, bars| {
        let mut s = build_strategy("rhai", &json!({ "script":
r#"let ema9   = ind.ema(9,    4);
let ema21  = ind.ema(21,   4);
let ema50  = ind.ema(50,   4);
let rsi14  = ind.rsi(14,   4);
let adx14  = ind.adx(14,   5);
let atr14  = ind.atr(14,   3);
let bb_u   = ind.bb_upper(20, 3);
let bb_l   = ind.bb_lower(20, 3);
let h1_ema = ind.ema(20, "H1", 3);

let trend   = adx14[0] > 25.0 && rising_n(adx14, 3);
let squeeze = (bb_u[0] - bb_l[0]) < atr14[0] * 1.5;
let h_break = highest(close, 20) == close[0];

if cross_above(ema9, ema21) && above(ema21, ema50)
   && rsi14[0] > 50.0 && rsi14[0] < 70.0
   && trend && squeeze && h_break
   && above(h1_ema, ema50) {
    entry = true;
    tp    = close[0] + atr14[0] * 2.5;
    sl    = close[0] - atr14[0] * 1.5;
}
if cross_below(ema9, ema21) || rsi14[0] > 80.0 || falling_n(adx14, 2) {
    exit = true;
}"#
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.finish();
}

criterion_group!(benches, bench_rsi_construct, bench_rsi_run, bench_ema_run, bench_multi_run, bench_kitchen_sink_run, bench_btc_m1_real);
criterion_main!(benches);
