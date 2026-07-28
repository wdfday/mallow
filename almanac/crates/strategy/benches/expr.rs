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
                let rsi14 = ind.rsi(14);\n\
                if rsi14[0] < 35.0 { entry = true; }\n\
                if rsi14[0] > 65.0 { exit  = true; }\n\
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
                let rsi14 = ind.rsi(14);\n\
                if rsi14[0] < 35.0 { entry = true; }\n\
                if rsi14[0] > 65.0 { exit  = true; }\n\
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

    group.bench_with_input(BenchmarkId::from_parameter("ind_script"), &bars, |b, bars| {
        let mut s = build_strategy("script", &json!({
            "script": "\
                let ema20 = ind.ema(20);\n\
                let ema50 = ind.ema(50);\n\
                if cross_above(ema20, ema50) { entry = true; }\n\
                if cross_below(ema20, ema50) { exit  = true; }\n\
            "
        })).unwrap();
        b.iter(|| { s.reset(); run_all(s.as_mut(), bars) })
    });

    // `ta.*` calls execute as genuine Rhai function dispatch every bar
    // (Map key lookup + write_lock downcast) instead of `ind.*`'s
    // pre-computed-array-in-Scope model — this measures that overhead
    // directly on an equivalent strategy.
    group.bench_with_input(BenchmarkId::from_parameter("ta_script"), &bars, |b, bars| {
        // Real newlines required — `ta.*` allows only one reference per
        // physical line (see `validate_ta_declarations`), unlike `ind.*`.
        let mut s = build_strategy("script", &json!({
            "script": "\
                let ema20 = ta.ema(20, close[0]);\n\
                let ema50 = ta.ema(50, close[0]);\n\
                if cross_above(ema20, ema50) { entry = true; }\n\
                if cross_below(ema20, ema50) { exit  = true; }\n\
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
                let rsi14 = ind.rsi(14);\n\
                let ema20 = ind.ema(20);\n\
                let ema50 = ind.ema(50);\n\
                let macd  = ind.macd(12);\n\
                if rsi14[0] < 40.0 && ema20[0] > ema50[0] && macd[0] > 0.0 { entry = true; }\n\
                if rsi14[0] > 60.0 || ema20[0] < ema50[0] { exit = true; }\n\
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

use alm_core::{MtfSnapshot, MtfStrategy, TfBarEvent, TfView, Timeframe};
use alm_strategy::script::v2::MtfScriptStrategy;
use std::collections::{HashMap, VecDeque};

fn run_all_mtf(s: &mut dyn MtfStrategy, bars: &[Bar]) -> usize {
    let h1_duration = Timeframe::H1.duration_ms();
    let mut h1_bars = Vec::new();
    let mut current_h1: Option<Bar> = None;
    for b in bars {
        let h1_ts = (b.timestamp / h1_duration) * h1_duration;
        if let Some(ref mut cur) = current_h1 {
            if cur.timestamp == h1_ts {
                cur.close = b.close;
                cur.high = cur.high.max(b.high);
                cur.low = cur.low.min(b.low);
                cur.volume += b.volume;
            } else {
                h1_bars.push(cur.clone());
                current_h1 = Some(Bar::new(h1_ts, &b.symbol, b.open, b.high, b.low, b.close, b.volume));
            }
        } else {
            current_h1 = Some(Bar::new(h1_ts, &b.symbol, b.open, b.high, b.low, b.close, b.volume));
        }
    }
    if let Some(cur) = current_h1 {
        h1_bars.push(cur);
    }

    let mut by_ts: std::collections::BTreeMap<i64, Vec<(Timeframe, Bar)>> = std::collections::BTreeMap::new();
    for b in bars {
        by_ts.entry(b.timestamp + Timeframe::M1.duration_ms()).or_default().push((Timeframe::M1, b.clone()));
    }
    for b in &h1_bars {
        by_ts.entry(b.timestamp + Timeframe::H1.duration_ms()).or_default().push((Timeframe::H1, b.clone()));
    }

    let mut confirmed: HashMap<Timeframe, VecDeque<Bar>> = HashMap::new();
    let mut count = 0;

    for (&close_ts, tick) in &by_ts {
        for (tf, b) in tick {
            confirmed.entry(*tf).or_default().push_back(b.clone());
        }
        let events: Vec<TfBarEvent<'_>> = tick.iter()
            .map(|(tf, b)| TfBarEvent { tf: *tf, bar: b })
            .collect();
        let views: HashMap<Timeframe, TfView<'_>> = confirmed.iter()
            .map(|(tf, w)| (*tf, TfView { tf: *tf, confirmed: w }))
            .collect();
        let snap = MtfSnapshot { base_tf: Timeframe::M1, close_ts, events: &events, views: &views };
        count += s.on_bars(black_box(snap)).len();
    }
    count
}

fn bench_kitchen_sink_run(c: &mut Criterion) {
    let bars = make_bars(1_000);
    let mut group = c.benchmark_group("kitchen_sink_run/1000bars");

    let named = KitchenSinkStrategy::new("BTCUSDT");
    let script = named
        .script()
        .expect("KitchenSinkStrategy::script() must return canonical RHAI_SCRIPT");

    group.bench_with_input(BenchmarkId::from_parameter("hardcoded"), &bars, |b, bars| {
        let mut s = KitchenSinkStrategy::new("BTCUSDT");
        b.iter(|| { s.reset(); run_all_mtf(&mut s, bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("script_full"), &bars, |b, bars| {
        let mut s = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        b.iter(|| { s.reset(); run_all_mtf(&mut s, bars) })
    });

    group.finish();
}

// ── BTC M1 real data ──────────────────────────────────────────────────────────
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

    let named = KitchenSinkStrategy::new("BTCUSDT");
    let script = named
        .script()
        .expect("KitchenSinkStrategy::script() must return canonical RHAI_SCRIPT");

    group.bench_with_input(BenchmarkId::from_parameter("hardcoded"), &bars, |b, bars| {
        let mut s = KitchenSinkStrategy::new("BTCUSDT");
        b.iter(|| { s.reset(); run_all_mtf(&mut s, bars) })
    });

    group.bench_with_input(BenchmarkId::from_parameter("script_full"), &bars, |b, bars| {
        let mut s = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();
        b.iter(|| { s.reset(); run_all_mtf(&mut s, bars) })
    });

    group.finish();
}

criterion_group!(benches, bench_rsi_construct, bench_rsi_run, bench_ema_run, bench_multi_run, bench_kitchen_sink_run, bench_btc_m1_real);
criterion_main!(benches);
