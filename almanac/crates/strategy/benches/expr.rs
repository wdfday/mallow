mod bench_utils;

/// Benchmark: CelStrategy vs DynamicStrategy vs RhaiStrategy vs hardcoded struct.
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

    // group.bench_function("dynamic", |b| b.iter(|| {
    //     build_strategy("dynamic", &json!({
    //         "indicators": { "rsi": { "type": "rsi", "period": 14 } },
    //         "entry": { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "lt", "value": 35.0 }]},
    //         "exit":  { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "gt", "value": 65.0 }]}
    //     })).unwrap()
    // }));

    group.bench_function("cel", |b| b.iter(|| {
        build_strategy("cel", &json!({
            "entry": "rsi(14) < 35.0",
            "exit":  "rsi(14) > 65.0"
        })).unwrap()
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

    // group.bench_with_input(BenchmarkId::from_parameter("dynamic"), &bars, |b, bars| {
    //     let mut s = build_strategy("dynamic", &json!({
    //         "indicators": { "rsi": { "type": "rsi", "period": 14 } },
    //         "entry": { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "lt", "value": 35.0 }]},
    //         "exit":  { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "gt", "value": 65.0 }]}
    //     })).unwrap();
    //     b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    // });

    group.bench_with_input(BenchmarkId::from_parameter("cel"), &bars, |b, bars| {
        let mut s = build_strategy("cel", &json!({
            "entry": "rsi(14) < 35.0",
            "exit":  "rsi(14) > 65.0"
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
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

    // group.bench_with_input(BenchmarkId::from_parameter("dynamic"), &bars, |b, bars| {
    //     let mut s = build_strategy("dynamic", &json!({
    //         "indicators": {
    //             "fast": { "type": "ema", "period": 20 },
    //             "slow": { "type": "ema", "period": 50 }
    //         },
    //         "entry": { "logic": "and", "rules": [{ "source": "fast", "field": "value", "op": "cross_above", "compare": "slow", "compare_field": "value" }]},
    //         "exit":  { "logic": "and", "rules": [{ "source": "fast", "field": "value", "op": "cross_below", "compare": "slow", "compare_field": "value" }]}
    //     })).unwrap();
    //     b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    // });

    group.bench_with_input(BenchmarkId::from_parameter("cel"), &bars, |b, bars| {
        let mut s = build_strategy("cel", &json!({
            "entry": "prev_ema(20) <= prev_ema(50) && ema(20) > ema(50)",
            "exit":  "prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)"
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

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

    // group.bench_with_input(BenchmarkId::from_parameter("dynamic"), &bars, |b, bars| {
    //     let mut s = build_strategy("dynamic", &json!({
    //         "indicators": {
    //             "rsi":  { "type": "rsi",    "period": 14 },
    //             "fast": { "type": "ema",    "period": 20 },
    //             "slow": { "type": "ema",    "period": 50 },
    //             "macd": { "type": "macd",   "fast": 12, "slow": 26, "signal": 9 },
    //             "bb":   { "type": "bbands", "period": 20, "multiplier": 2.0 }
    //         },
    //         "entry": { "logic": "and", "rules": [
    //             { "source": "rsi",  "field": "value",     "op": "lt", "value": 40.0 },
    //             { "source": "fast", "field": "value",     "op": "gt", "compare": "slow", "compare_field": "value" },
    //             { "source": "macd", "field": "histogram", "op": "gt", "value": 0.0 }
    //         ]},
    //         "exit": { "logic": "or", "rules": [
    //             { "source": "rsi",  "field": "value", "op": "gt", "value": 60.0 },
    //             { "source": "fast", "field": "value", "op": "lt", "compare": "slow", "compare_field": "value" }
    //         ]}
    //     })).unwrap();
    //     b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    // });

    group.bench_with_input(BenchmarkId::from_parameter("cel"), &bars, |b, bars| {
        let mut s = build_strategy("cel", &json!({
            "entry": "rsi(14) < 40.0 && ema(20) > ema(50) && macd_hist(12) > 0.0",
            "exit":  "rsi(14) > 60.0 || ema(20) < ema(50)"
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

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
// Compares the three flavours on a compute-heavy 10-indicator + MTF strategy:
//   hardcoded  — pure Rust struct, zero interpreter overhead
//   cel        — ~60% approximation (no highest/rising_n/MTF/tp-sl in CEL)
//   rhai       — 100% faithful to the spec script, runs full Rhai VM

fn bench_kitchen_sink_run(c: &mut Criterion) {
    let bars = make_bars(1_000);
    let mut group = c.benchmark_group("kitchen_sink_run/1000bars");

    group.bench_with_input(BenchmarkId::from_parameter("hardcoded"), &bars, |b, bars| {
        let mut s = KitchenSinkStrategy::new();
        b.iter(|| { s.reset(); run_all(&mut s, bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("cel_approx60"), &bars, |b, bars| {
        // CEL cannot express: highest(close,20), rising_n, momentum, H1 MTF, tp/sl.
        // This approximation covers the M1 indicator logic only (~60% of conditions).
        let mut s = build_strategy("cel", &json!({
            "entry": "cross_above(ema(9), ema(21)) \
                   && ema(21) > ema(50) \
                   && rsi(14) > 50.0 && rsi(14) < 70.0 \
                   && adx(14) > 25.0 \
                   && (bb_upper(20) - bb_lower(20)) < atr(14) * 1.5",
            "exit":  "cross_below(ema(9), ema(21)) || rsi(14) > 80.0"
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
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
