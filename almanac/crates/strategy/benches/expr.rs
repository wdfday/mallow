mod bench_utils;

/// Benchmark: CelStrategy vs DynamicStrategy vs hardcoded struct.
///
/// Tách rõ 2 phần:
/// - construct - thời gian tạo strategy (1 lần)
/// - run - thời gian xử lý 1000 bar (vòng lặp thực sự)
///
/// Chạy:
/// - cargo bench -p alm-strategy
/// - cargo bench -p alm-strategy -- rsi_run   (chỉ xem group run)

use alm_core::{bar::Bar, strategy::Strategy};
use alm_strategy::{factory::build_strategy, named::rsi_mean_rev::RsiMeanRev};
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

    group.bench_function("dynamic", |b| b.iter(|| {
        build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "lt", "value": 35.0 }]},
            "exit":  { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "gt", "value": 65.0 }]}
        })).unwrap()
    }));

    group.bench_function("cel", |b| b.iter(|| {
        build_strategy("cel", &json!({
            "entry": "rsi(14) < 35.0",
            "exit":  "rsi(14) > 65.0"
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

    group.bench_with_input(BenchmarkId::from_parameter("dynamic"), &bars, |b, bars| {
        let mut s = build_strategy("dynamic", &json!({
            "indicators": { "rsi": { "type": "rsi", "period": 14 } },
            "entry": { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "lt", "value": 35.0 }]},
            "exit":  { "logic": "and", "rules": [{ "source": "rsi", "field": "value", "op": "gt", "value": 65.0 }]}
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("cel"), &bars, |b, bars| {
        let mut s = build_strategy("cel", &json!({
            "entry": "rsi(14) < 35.0",
            "exit":  "rsi(14) > 65.0"
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.finish();
}

// ── EMA crossover ─────────────────────────────────────────────────────────────

fn bench_ema_run(c: &mut Criterion) {
    let bars = make_bars(1_000);
    let mut group = c.benchmark_group("ema_cross_run/1000bars");

    group.bench_with_input(BenchmarkId::from_parameter("dynamic"), &bars, |b, bars| {
        let mut s = build_strategy("dynamic", &json!({
            "indicators": {
                "fast": { "type": "ema", "period": 20 },
                "slow": { "type": "ema", "period": 50 }
            },
            "entry": { "logic": "and", "rules": [{ "source": "fast", "field": "value", "op": "cross_above", "compare": "slow", "compare_field": "value" }]},
            "exit":  { "logic": "and", "rules": [{ "source": "fast", "field": "value", "op": "cross_below", "compare": "slow", "compare_field": "value" }]}
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("cel"), &bars, |b, bars| {
        let mut s = build_strategy("cel", &json!({
            "entry": "prev_ema(20) <= prev_ema(50) && ema(20) > ema(50)",
            "exit":  "prev_ema(20) >= prev_ema(50) && ema(20) < ema(50)"
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.finish();
}

// ── Multi-indicator ───────────────────────────────────────────────────────────

fn bench_multi_run(c: &mut Criterion) {
    let bars = make_bars(1_000);
    let mut group = c.benchmark_group("multi_run/1000bars");

    group.bench_with_input(BenchmarkId::from_parameter("dynamic"), &bars, |b, bars| {
        let mut s = build_strategy("dynamic", &json!({
            "indicators": {
                "rsi":  { "type": "rsi",    "period": 14 },
                "fast": { "type": "ema",    "period": 20 },
                "slow": { "type": "ema",    "period": 50 },
                "macd": { "type": "macd",   "fast": 12, "slow": 26, "signal": 9 },
                "bb":   { "type": "bbands", "period": 20, "multiplier": 2.0 }
            },
            "entry": { "logic": "and", "rules": [
                { "source": "rsi",  "field": "value",     "op": "lt", "value": 40.0 },
                { "source": "fast", "field": "value",     "op": "gt", "compare": "slow", "compare_field": "value" },
                { "source": "macd", "field": "histogram", "op": "gt", "value": 0.0 }
            ]},
            "exit": { "logic": "or", "rules": [
                { "source": "rsi",  "field": "value", "op": "gt", "value": 60.0 },
                { "source": "fast", "field": "value", "op": "lt", "compare": "slow", "compare_field": "value" }
            ]}
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("cel"), &bars, |b, bars| {
        let mut s = build_strategy("cel", &json!({
            "entry": "rsi(14) < 40.0 && ema(20) > ema(50) && macd_hist(12) > 0.0",
            "exit":  "rsi(14) > 60.0 || ema(20) < ema(50)"
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    group.finish();
}

criterion_group!(benches, bench_rsi_construct, bench_rsi_run, bench_ema_run, bench_multi_run);
criterion_main!(benches);
