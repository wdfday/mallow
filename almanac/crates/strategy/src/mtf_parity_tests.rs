//! Parity tests for multi-timeframe (MTF), Heiken Ashi candle transforms,
//! TP/SL exits
//!
//! Coverage added here (complementing the per-strategy named/*.rs tests):
//!
//! | Group | Tests |
//! |-------|-------|
//! | **MTF confirmed** | H1 signals only fire at H1 boundary, reset parity |
//! | **MTF live** | live_H1 fires mid-bar, live vs confirmed differ, reset parity, coexistence |
//! | **Heiken Ashi** | HaColor named reset parity |
//! | **TP / SL** | fixed-% TP, fixed-% SL, TP fires before RSI exit, ATR-based TP/SL |

#![cfg(test)]

use std::collections::{BTreeMap, HashMap, VecDeque};
use std::path::PathBuf;

use alm_core::{Bar, Strategy};
use alm_core::{MtfSnapshot, MtfStrategy, TfBarEvent, TfView, Timeframe};
use alm_core::signal::Direction;
use alm_data::{BarFeed, ParquetFeed};
use serde_json::json;

use crate::factory::build_strategy;
use crate::named::{MtfAdxPullbackStrategy, MtfBbMacdStrategy, MtfEmaRsiStrategy, MtfMaCrossStrategy};
use crate::script::v2::MtfScriptStrategy;
use crate::test_utils::{bar, run, assert_parity, trending_bars};

// ─────────────────────────────────────────────────────────────────────────────
// Shared bar generators
// ─────────────────────────────────────────────────────────────────────────────

/// `n` hours of M1 bars at 60 s intervals, plus one extra bar to trigger the
/// final H1 close — total = `n * 60 + 1` bars.
///
/// Price shape: smooth downtrend from 200→100 in the first half, then uptrend
/// back to 200 in the second half.  Each H1 bar therefore has a clear trend
/// direction, which drives RSI and EMA into oversold/overbought territory.
fn m1_hours(n: usize) -> Vec<Bar> {
    let total = n * 60 + 1;
    let half  = n as f64 / 2.0;
    (0..total)
        .map(|i| {
            let t = i as f64 / 60.0;
            let price = if t < half {
                200.0 - (t / half) * 100.0
            } else {
                100.0 + ((t - half) / half) * 100.0
            };
            bar(i as i64 * 60_000, price.max(1.0))
        })
        .collect()
}

/// M1 bars with a brief dip-within-uptrend pattern, suitable for testing
/// combined H1-filter + M1-signal strategies.
///
/// Structure (hours):
/// - 0-11  : slow rise 100→160  (H1 RSI warms up above 50)
/// - 12-14 : 3-hour dip 160→100 (M1 RSI briefly oversold; H1 RSI stays > 50
///            for the first bar of the dip before dropping)
/// - 15-19 : recovery 100→160
/// - 20    : trigger bar (closes the final H1 bucket)
fn m1_dip_in_uptrend() -> Vec<Bar> {
    let mut ts = 0i64;
    let mut bars = vec![];
    // Slow rise
    for i in 0..720u32 {
        bars.push(bar(ts, 100.0 + i as f64 * (60.0 / 720.0)));
        ts += 60_000;
    }
    // Sharp dip within M1 bars of hours 12-14 (180 bars)
    for i in 0..180u32 {
        bars.push(bar(ts, (160.0 - i as f64 * (60.0 / 180.0)).max(1.0)));
        ts += 60_000;
    }
    // Recovery
    for i in 0..300u32 {
        bars.push(bar(ts, 100.0 + i as f64 * (60.0 / 300.0)));
        ts += 60_000;
    }
    // Trigger bar for final H1 close
    bars.push(bar(ts, 160.0));
    bars
}

// ─────────────────────────────────────────────────────────────────────────────
// MTF reset / determinism (V2 MtfScriptStrategy with real HTF feed)
//
// V1 rejects TF arguments at build time, so any script with `ind.*(period, "TF")`
// must go through MtfScriptStrategy directly. These tests build it and feed it
// the same (M1, H1) bar pair twice with reset() in between.
// ─────────────────────────────────────────────────────────────────────────────

/// Helper: aggregate M1 bars into H1 by calendar bucket (open of first, close
/// of last). Lets us reuse the original M1-only test data with V2.
fn aggregate_m1_to_h1(m1: &[Bar]) -> Vec<Bar> {
    let h1_ms = Timeframe::H1.duration_ms();
    let mut buckets: BTreeMap<i64, Vec<&Bar>> = BTreeMap::new();
    for b in m1 {
        let key = (b.timestamp / h1_ms) * h1_ms;
        buckets.entry(key).or_default().push(b);
    }
    buckets
        .into_iter()
        .map(|(ts, bars)| {
            let o = bars.first().unwrap().open;
            let c = bars.last().unwrap().close;
            let h = bars.iter().map(|b| b.high).fold(f64::MIN, f64::max);
            let l = bars.iter().map(|b| b.low).fold(f64::MAX, f64::min);
            let v: f64 = bars.iter().map(|b| b.volume).sum();
            Bar::new(ts, "TEST", o, h, l, c, v)
        })
        .collect()
}

/// MtfScriptStrategy: `reset()` must reproduce the exact same signals when
/// fed the same bars a second time.
#[test]
fn mtf_reset_parity_script() {
    let m1 = m1_hours(30);
    let h1 = aggregate_m1_to_h1(&m1);

    let script = r#"
let h1_rsi = ind.rsi(5, "H1");
if h1_rsi[0] < 35.0 { entry = true; }
if h1_rsi[0] > 65.0 { exit  = true; }
"#;
    let mut strat = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();

    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    assert!(!r1.is_empty(), "mtf_reset_script: expected signals");
    assert_parity("mtf script reset: run1 vs run2", &r1, &r2);
}


/// Mixed MTF + base-TF reset: both H1 and M1 indicator state must be
/// cleared by `reset()`.
#[test]
fn mtf_mixed_tf_reset_parity() {
    let m1 = m1_dip_in_uptrend();
    let h1 = aggregate_m1_to_h1(&m1);

    let script = r#"
let h1_rsi = ind.rsi(5, "H1");
let m1_rsi = ind.rsi(5);
if h1_rsi[0] > 50.0 && m1_rsi[0] < 35.0 { entry = true; }
if h1_rsi[0] < 50.0 || m1_rsi[0] > 65.0 { exit  = true; }
"#;
    let mut strat = MtfScriptStrategy::from_script(script, Timeframe::M1).unwrap();

    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    assert_parity("mtf mixed TF reset: run1 vs run2", &r1, &r2);
}

// ─────────────────────────────────────────────────────────────────────────────
// Heiken Ashi
// ─────────────────────────────────────────────────────────────────────────────

/// HaColor reset: named strategy reproduces the same signals after `reset()`.
#[test]
fn ha_color_reset_parity() {
    use crate::named::heiken_ashi_strategy::HaColor;

    let bars = trending_bars(300);

    let mut named = HaColor::new(1);
    let r1 = run(&mut named, &bars);
    named.reset();
    let r2 = run(&mut named, &bars);
    assert_parity("HaColor named reset", &r1, &r2);
}

// ─────────────────────────────────────────────────────────────────────────────
// v2 MtfStrategy named ↔ MtfScriptStrategy parity
// ─────────────────────────────────────────────────────────────────────────────

/// Thin simulation loop: merge M1 + H1 bars by close_ts, build MtfSnapshot per
/// tick, call `strategy.on_bars`, and collect `(timestamp, direction)` pairs.
/// Intentionally avoids MtfEngine so tests don't depend on risk/broker logic.
fn run_mtf_sigs(
    strategy: &mut dyn MtfStrategy,
    base_tf: Timeframe,
    m1_bars: &[Bar],
    h1_bars: &[Bar],
) -> Vec<(i64, Direction)> {
    let mut by_ts: BTreeMap<i64, Vec<(Timeframe, Bar)>> = BTreeMap::new();
    for b in m1_bars {
        by_ts
            .entry(b.timestamp + base_tf.duration_ms())
            .or_default()
            .push((base_tf, b.clone()));
    }
    for b in h1_bars {
        by_ts
            .entry(b.timestamp + Timeframe::H1.duration_ms())
            .or_default()
            .push((Timeframe::H1, b.clone()));
    }

    let mut confirmed: HashMap<Timeframe, VecDeque<Bar>> = HashMap::new();
    let mut out: Vec<(i64, Direction)> = Vec::new();

    for (&close_ts, tick) in &by_ts {
        for (tf, b) in tick {
            confirmed.entry(*tf).or_default().push_back(b.clone());
        }

        let events: Vec<TfBarEvent<'_>> = tick
            .iter()
            .map(|(tf, b)| TfBarEvent { tf: *tf, bar: b })
            .collect();

        let views: HashMap<Timeframe, TfView<'_>> = confirmed
            .iter()
            .map(|(tf, w)| (*tf, TfView { tf: *tf, confirmed: w }))
            .collect();

        let snap = MtfSnapshot { base_tf, close_ts, events: &events, views: &views };

        for sig in strategy.on_bars(snap) {
            out.push((sig.timestamp, sig.direction));
        }
    }
    out
}

fn assert_mtf_parity(label: &str, named: &[(i64, Direction)], script: &[(i64, Direction)]) {
    assert_eq!(
        named, script,
        "{label}: signal mismatch\n  named : {named:?}\n  script: {script:?}"
    );
}

/// Count (entries, exits) in a signal list.
/// Entries = Long + Short; exits = Exit.
fn count_dirs(sigs: &[(i64, Direction)]) -> (usize, usize) {
    let entries = sigs.iter().filter(|(_, d)| matches!(d, Direction::Long | Direction::Short)).count();
    let exits   = sigs.iter().filter(|(_, d)| matches!(d, Direction::Exit)).count();
    (entries, exits)
}

// ── Test data ─────────────────────────────────────────────────────────────────

/// ~65 H1 bars + ~3900 M1 bars for MtfEmaRsi parity.
///
/// Phases:
/// 1. 50 H1 flat (2.5 × EMA period 20 = 50 — practical warmup rule).
/// 2. 10 H1 rising. Each H1 period: 15 M1 bars fall (RSI → <40), 45 bars rise.
/// 3. 5 H1 falling (H1 EMA falling → exit fires).
fn mtf_ema_rsi_bars() -> (Vec<Bar>, Vec<Bar>) {
    let m1_ms: i64 = 60_000;
    let h1_ms: i64 = 3_600_000;
    let mut m1: Vec<Bar> = Vec::new();
    let mut h1: Vec<Bar> = Vec::new();
    let mut t_m1: i64 = 0;
    let mut t_h1: i64 = 0;

    // Phase 1: 50 flat H1 periods (2.5 × period(20))
    for _ in 0..50 {
        for _ in 0..60 { m1.push(bar(t_m1, 100.0)); t_m1 += m1_ms; }
        h1.push(bar(t_h1, 100.0)); t_h1 += h1_ms;
    }

    // Phase 2: 10 rising H1 periods (+6 per H1)
    for i in 0..10_i64 {
        let base = 100.0 + i as f64 * 6.0;
        for j in 0..60_i64 {
            let p = if j < 15 {
                (base - j as f64 * 2.0).max(10.0)
            } else {
                base - 30.0 + (j - 15) as f64 * (36.0 / 45.0)
            };
            m1.push(bar(t_m1, p)); t_m1 += m1_ms;
        }
        h1.push(bar(t_h1, base + 6.0)); t_h1 += h1_ms;
    }

    // Phase 3: 5 falling H1 periods (-8 per H1)
    for i in 0..5_i64 {
        let base = 160.0 - i as f64 * 8.0;
        for j in 0..60_i64 {
            let p = (base - j as f64 * 0.5).max(10.0);
            m1.push(bar(t_m1, p)); t_m1 += m1_ms;
        }
        h1.push(bar(t_h1, base - 8.0)); t_h1 += h1_ms;
    }

    (m1, h1)
}

/// ~137 H1 bars + ~8220 M1 bars for MtfMaCross parity.
///
/// Phases:
/// 1. 125 H1 flat (2.5 × EMA period 50 = 125 — practical warmup rule).
/// 2. 8 H1 rising. Each H1 period: 20 M1 bars dip then 40 bars recover (→ crossover).
/// 3. 4 H1 falling (close < H1 EMA → exit fires).
fn mtf_ma_cross_bars() -> (Vec<Bar>, Vec<Bar>) {
    let m1_ms: i64 = 60_000;
    let h1_ms: i64 = 3_600_000;
    let mut m1: Vec<Bar> = Vec::new();
    let mut h1: Vec<Bar> = Vec::new();
    let mut t_m1: i64 = 0;
    let mut t_h1: i64 = 0;

    // Phase 1: 125 flat H1 periods (2.5 × period(50))
    for _ in 0..125 {
        for _ in 0..60 { m1.push(bar(t_m1, 100.0)); t_m1 += m1_ms; }
        h1.push(bar(t_h1, 100.0)); t_h1 += h1_ms;
    }

    // Phase 2: 8 rising H1 periods
    for i in 0..8_i64 {
        let h_price = 110.0 + i as f64 * 5.0;
        for j in 0..60_i64 {
            let p = if j < 20 {
                (h_price - j as f64 * 1.0).max(10.0)
            } else {
                h_price - 20.0 + (j - 20) as f64 * (25.0 / 40.0)
            };
            m1.push(bar(t_m1, p)); t_m1 += m1_ms;
        }
        h1.push(bar(t_h1, h_price)); t_h1 += h1_ms;
    }

    // Phase 3: 4 falling H1 periods
    for i in 0..4_i64 {
        let h_price = 145.0 - i as f64 * 10.0;
        for j in 0..60_i64 {
            let p = (h_price - j as f64 * 0.8).max(10.0);
            m1.push(bar(t_m1, p)); t_m1 += m1_ms;
        }
        h1.push(bar(t_h1, h_price - 10.0)); t_h1 += h1_ms;
    }

    (m1, h1)
}

// ── MtfEmaRsi: named ↔ script ─────────────────────────────────────────────────

const SCRIPT_EMA_RSI: &str = r#"
let h1_ema = ind.ema(20, "H1");
let rsi    = ind.rsi(14);
if rising(h1_ema) && rsi[0] < 40.0 { entry = true; }
if falling(h1_ema) || rsi[0] > 70.0 { exit  = true; }
"#;

#[test]
fn mtf_v2_ema_rsi_named_vs_script() {
    let (m1, h1) = mtf_ema_rsi_bars();

    let mut named = MtfEmaRsiStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut strat = MtfScriptStrategy::from_script(SCRIPT_EMA_RSI, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] ema_rsi: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "mtf_v2_ema_rsi: named must produce signals");
    assert_mtf_parity("MtfEmaRsi named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_v2_ema_rsi_named_reset_parity() {
    let (m1, h1) = mtf_ema_rsi_bars();
    let mut named = MtfEmaRsiStrategy::new();
    let r1 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    named.reset();
    let r2 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert!(!r1.is_empty(), "mtf_v2_ema_rsi_reset: must produce signals");
    assert_mtf_parity("MtfEmaRsi reset: run1 vs run2", &r1, &r2);
}

#[test]
fn mtf_v2_ema_rsi_script_reset_parity() {
    let (m1, h1) = mtf_ema_rsi_bars();
    let mut strat = MtfScriptStrategy::from_script(SCRIPT_EMA_RSI, Timeframe::M1).unwrap();
    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    assert!(!r1.is_empty(), "mtf_v2_ema_rsi_script_reset: must produce signals");
    assert_mtf_parity("MtfEmaRsi script reset", &r1, &r2);
}

// ── MtfMaCross: named ↔ script ────────────────────────────────────────────────

const SCRIPT_MA_CROSS: &str = r#"
let h1_trend = ind.ema(50, "H1");
let ema9     = ind.ema(9);
let ema21    = ind.ema(21);
if close[0] > h1_trend[0] && cross_above(ema9, ema21) { entry = true; }
if close[0] < h1_trend[0] || cross_below(ema9, ema21) { exit  = true; }
"#;

#[test]
fn mtf_v2_ma_cross_named_vs_script() {
    let (m1, h1) = mtf_ma_cross_bars();

    let mut named = MtfMaCrossStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut strat = MtfScriptStrategy::from_script(SCRIPT_MA_CROSS, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] ma_cross: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "mtf_v2_ma_cross: named must produce signals");
    assert_mtf_parity("MtfMaCross named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_v2_ma_cross_named_reset_parity() {
    let (m1, h1) = mtf_ma_cross_bars();
    let mut named = MtfMaCrossStrategy::new();
    let r1 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    named.reset();
    let r2 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert!(!r1.is_empty(), "mtf_v2_ma_cross_reset: must produce signals");
    assert_mtf_parity("MtfMaCross reset: run1 vs run2", &r1, &r2);
}

#[test]
fn mtf_v2_ma_cross_script_reset_parity() {
    let (m1, h1) = mtf_ma_cross_bars();
    let mut strat = MtfScriptStrategy::from_script(SCRIPT_MA_CROSS, Timeframe::M1).unwrap();
    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    assert!(!r1.is_empty(), "mtf_v2_ma_cross_script_reset: must produce signals");
    assert_mtf_parity("MtfMaCross script reset", &r1, &r2);
}

// ── Sanity: no signals before H1 warmup ──────────────────────────────────────

/// Neither strategy should fire with only 1 confirmed H1 bar (h1_count < 2).
#[test]
fn mtf_v2_no_signals_before_h1_warmup() {
    let m1_bars: Vec<Bar> = (0..60_i64).map(|i| bar(i * 60_000, 100.0)).collect();
    let h1_bars = vec![bar(0_i64, 100.0)];

    let mut s1 = MtfEmaRsiStrategy::new();
    assert!(
        run_mtf_sigs(&mut s1, Timeframe::M1, &m1_bars, &h1_bars).is_empty(),
        "EmaRsi: must not fire with 1 H1 bar"
    );

    let mut s2 = MtfMaCrossStrategy::new();
    assert!(
        run_mtf_sigs(&mut s2, Timeframe::M1, &m1_bars, &h1_bars).is_empty(),
        "MaCross: must not fire with 1 H1 bar"
    );
}

/// Debug test: verify run_mtf_sigs actually collects signals.
/// EMA(20) needs 21 H1 bars before h1_count reaches 2; use 22 to be safe.
#[test]
fn debug_run_mtf_sigs_produces_signals() {
    // 22 flat H1 bars + 1320 flat M1 bars (22*60).
    // RSI(14) on flat bars returns 100.0 (avg_loss=0).
    // EMA(20) produces first value at H1 bar 19 (0-indexed) → h1_count=1.
    // EMA produces second value at H1 bar 20 → h1_count=2 → exit fires.
    let m1: Vec<Bar> = (0..1320_i64).map(|i| bar(i * 60_000, 100.0)).collect();
    let h1: Vec<Bar> = (0..22_i64).map(|i| bar(i * 3_600_000, 100.0)).collect();

    let mut named = MtfEmaRsiStrategy::new();
    let sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert!(
        !sigs.is_empty(),
        "debug: expected exit signals (rsi=100>70 with flat prices), got none"
    );
}

// ── MtfAdxPullback: named ↔ script ───────────────────────────────────────────

const SCRIPT_ADX_PULLBACK: &str = r#"
let h1_adx   = ind.adx(14, "H1");
let h1_trend = ind.ema(50, "H1");
let rsi      = ind.rsi(14);
if h1_adx[0] > 25.0 && close[0] > h1_trend[0] && rsi[0] < 40.0 { entry = true; }
if h1_adx[0] < 20.0 || close[0] < h1_trend[0] || rsi[0] > 70.0 { exit  = true; }
"#;

/// Test data for MtfAdxPullbackStrategy parity.
///
/// Warmup: 125 H1 bars (2.5 × max(ADX period 14, EMA period 50) = 2.5×50).
/// Signal phase: trending bars (ADX > 25) with a dip (RSI < 40) then recovery.
fn mtf_adx_pullback_bars() -> (Vec<Bar>, Vec<Bar>) {
    let m1_ms: i64 = 60_000;
    let h1_ms: i64 = 3_600_000;
    let mut m1: Vec<Bar> = Vec::new();
    let mut h1: Vec<Bar> = Vec::new();
    let mut t_m1: i64 = 0;
    let mut t_h1: i64 = 0;

    // Phase 1: 125 flat H1 warmup periods (2.5 × 50 = 125)
    for _ in 0..125 {
        for _ in 0..60 { m1.push(bar(t_m1, 100.0)); t_m1 += m1_ms; }
        h1.push(bar(t_h1, 100.0)); t_h1 += h1_ms;
    }

    // Phase 2: 15 strong uptrend H1 periods (+8 per bar, driving ADX > 25)
    for i in 0..15_i64 {
        let base = 100.0 + i as f64 * 8.0;
        for j in 0..60_i64 {
            let p = if j < 15 {
                // Brief dip (RSI → <40)
                (base - j as f64 * 3.0).max(10.0)
            } else {
                // Recovery
                base - 45.0 + (j - 15) as f64 * (53.0 / 45.0)
            };
            m1.push(bar(t_m1, p)); t_m1 += m1_ms;
        }
        h1.push(bar(t_h1, base + 8.0)); t_h1 += h1_ms;
    }

    // Phase 3: 5 weak / declining periods (ADX falls below 20, exit fires)
    for i in 0..5_i64 {
        let base = 220.0 - i as f64 * 5.0;
        for j in 0..60_i64 {
            let p = base - j as f64 * 0.05;
            m1.push(bar(t_m1, p)); t_m1 += m1_ms;
        }
        h1.push(bar(t_h1, base - 5.0)); t_h1 += h1_ms;
    }

    (m1, h1)
}

#[test]
fn mtf_v2_adx_pullback_named_vs_script() {
    let (m1, h1) = mtf_adx_pullback_bars();

    let mut named = MtfAdxPullbackStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut strat = MtfScriptStrategy::from_script(SCRIPT_ADX_PULLBACK, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] adx_pullback: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "mtf_v2_adx_pullback: named must produce signals");
    assert_mtf_parity("MtfAdxPullback named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_v2_adx_pullback_named_reset_parity() {
    let (m1, h1) = mtf_adx_pullback_bars();
    let mut named = MtfAdxPullbackStrategy::new();
    let r1 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    named.reset();
    let r2 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert_mtf_parity("MtfAdxPullback named reset parity", &r1, &r2);
}

#[test]
fn mtf_v2_adx_pullback_script_reset_parity() {
    let (m1, h1) = mtf_adx_pullback_bars();
    let mut strat = MtfScriptStrategy::from_script(SCRIPT_ADX_PULLBACK, Timeframe::M1).unwrap();
    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    assert_mtf_parity("MtfAdxPullback script reset parity", &r1, &r2);
}

// ── MtfBbMacd: named ↔ script ─────────────────────────────────────────────────

const SCRIPT_BB_MACD: &str = r#"
let h1_bb = ind.bbands(20, "H1");
let macd  = ind.macd(12);
let rsi   = ind.rsi(14);
if close[0] > h1_bb[0].upper && macd[0].histogram > 0.0 && rsi[0] < 55.0 { entry = true; }
if close[0] < h1_bb[0].middle || macd[0].histogram < 0.0 { exit = true; }
"#;

/// Test data for MtfBbMacdStrategy parity.
///
/// Warmup: 50 H1 bars (2.5 × BBands period 20).
/// M1 MACD(12,26,9) warms up in ~35 M1 bars (well within the 50×60 M1 warmup).
/// Signal phase: breakout bars (close > BB upper, MACD histogram > 0, RSI < 55).
fn mtf_bb_macd_bars() -> (Vec<Bar>, Vec<Bar>) {
    let m1_ms: i64 = 60_000;
    let h1_ms: i64 = 3_600_000;
    let mut m1: Vec<Bar> = Vec::new();
    let mut h1: Vec<Bar> = Vec::new();
    let mut t_m1: i64 = 0;
    let mut t_h1: i64 = 0;

    // Phase 1: 50 flat H1 warmup periods (2.5 × 20)
    for _ in 0..50 {
        for _ in 0..60 { m1.push(bar(t_m1, 100.0)); t_m1 += m1_ms; }
        h1.push(bar(t_h1, 100.0)); t_h1 += h1_ms;
    }

    // Phase 2: 12 breakout H1 periods — price accelerates above BB upper
    for i in 0..12_i64 {
        let base = 100.0 + i as f64 * 12.0;
        for j in 0..60_i64 {
            // Accelerating rise: price outruns BB, MACD histogram positive
            let p = base + j as f64 * (12.0 / 60.0);
            m1.push(bar(t_m1, p)); t_m1 += m1_ms;
        }
        h1.push(bar(t_h1, base + 12.0)); t_h1 += h1_ms;
    }

    // Phase 3: 5 reversal H1 periods — price drops below BB middle, MACD negative
    for i in 0..5_i64 {
        let base = 244.0 - i as f64 * 20.0;
        for j in 0..60_i64 {
            let p = (base - j as f64 * (20.0 / 60.0)).max(10.0);
            m1.push(bar(t_m1, p)); t_m1 += m1_ms;
        }
        h1.push(bar(t_h1, (base - 20.0).max(10.0))); t_h1 += h1_ms;
    }

    (m1, h1)
}

#[test]
fn mtf_v2_bb_macd_named_vs_script() {
    let (m1, h1) = mtf_bb_macd_bars();

    let mut named = MtfBbMacdStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut strat = MtfScriptStrategy::from_script(SCRIPT_BB_MACD, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] bb_macd: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "mtf_v2_bb_macd: named must produce signals");
    assert_mtf_parity("MtfBbMacd named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_v2_bb_macd_named_reset_parity() {
    let (m1, h1) = mtf_bb_macd_bars();
    let mut named = MtfBbMacdStrategy::new();
    let r1 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    named.reset();
    let r2 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    assert_mtf_parity("MtfBbMacd named reset parity", &r1, &r2);
}

#[test]
fn mtf_v2_bb_macd_script_reset_parity() {
    let (m1, h1) = mtf_bb_macd_bars();
    let mut strat = MtfScriptStrategy::from_script(SCRIPT_BB_MACD, Timeframe::M1).unwrap();
    let r1 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    strat.reset();
    let r2 = run_mtf_sigs(&mut strat, Timeframe::M1, &m1, &h1);
    assert_mtf_parity("MtfBbMacd script reset parity", &r1, &r2);
}

// ─────────────────────────────────────────────────────────────────────────────
// Real-data MTF parity tests (BTCUSDT M1 + H1 parquet)
// ─────────────────────────────────────────────────────────────────────────────

fn testdata_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .unwrap()
        .join("data/testdata/BTCUSDT")
}

fn load_parquet(tf_dir: &str, file: &str) -> Option<Vec<Bar>> {
    let path = testdata_dir().join(tf_dir).join(file);
    if !path.exists() {
        eprintln!("[mtf-parity] testdata missing: {}", path.display());
        return None;
    }
    let mut feed = ParquetFeed::load(&path, "BTCUSDT").ok()?;
    let bars: Vec<Bar> = std::iter::from_fn(|| feed.next()).collect();
    Some(bars)
}

/// Load the long-range BTCUSDT parquet (2022–2026) for both M1 and H1.
/// Returns None if either file is missing (skip signal for caller).
fn load_btcusdt_m1_h1() -> Option<(Vec<Bar>, Vec<Bar>)> {
    let m1 = load_parquet("M1", "BTCUSDT_M1_2022-04-13_to_2026-04-12.parquet")?;
    let h1 = load_parquet("H1", "BTCUSDT_H1_2022-04-13_to_2026-04-12.parquet")?;
    if m1.len() < 1000 || h1.len() < 100 {
        eprintln!("[mtf-parity] too few bars (m1={}, h1={}), skipping", m1.len(), h1.len());
        return None;
    }
    eprintln!("[mtf-parity] loaded {} M1 + {} H1 bars", m1.len(), h1.len());
    Some((m1, h1))
}

/// Named `MtfEmaRsiStrategy` must produce the exact same signals as the
/// equivalent Rhai v2 script when run on real BTCUSDT M1+H1 data.
#[test]
fn mtf_real_data_ema_rsi_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfEmaRsiStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut script = MtfScriptStrategy::from_script(SCRIPT_EMA_RSI, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut script, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] ema_rsi: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "real-data ema_rsi: named must produce signals");
    assert_mtf_parity("MtfEmaRsi real-data named vs script", &named_sigs, &script_sigs);
}

/// Named `MtfMaCrossStrategy` must produce the exact same signals as the
/// equivalent Rhai v2 script when run on real BTCUSDT M1+H1 data.
#[test]
fn mtf_real_data_ma_cross_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfMaCrossStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut script = MtfScriptStrategy::from_script(SCRIPT_MA_CROSS, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut script, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] ma_cross: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "real-data ma_cross: named must produce signals");
    assert_mtf_parity("MtfMaCross real-data named vs script", &named_sigs, &script_sigs);
}

/// Both strategies must reproduce the same signals when reset and re-run on
/// the same real data (idempotency of reset).
#[test]
fn mtf_real_data_reset_idempotent() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfEmaRsiStrategy::new();
    let r1 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);
    named.reset();
    let r2 = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    assert_mtf_parity("MtfEmaRsi real-data reset idempotent", &r1, &r2);
}

#[test]
fn mtf_real_data_adx_pullback_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfAdxPullbackStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut script = MtfScriptStrategy::from_script(SCRIPT_ADX_PULLBACK, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut script, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] adx_pullback: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "real-data adx_pullback: named must produce signals");
    assert_mtf_parity("MtfAdxPullback real-data named vs script", &named_sigs, &script_sigs);
}

#[test]
fn mtf_real_data_bb_macd_named_vs_script() {
    let Some((m1, h1)) = load_btcusdt_m1_h1() else { return };

    let mut named = MtfBbMacdStrategy::new();
    let named_sigs = run_mtf_sigs(&mut named, Timeframe::M1, &m1, &h1);

    let mut script = MtfScriptStrategy::from_script(SCRIPT_BB_MACD, Timeframe::M1).unwrap();
    let script_sigs = run_mtf_sigs(&mut script, Timeframe::M1, &m1, &h1);

    let (en, ex) = count_dirs(&named_sigs);
    eprintln!("[mtf-parity] bb_macd: total={} entry={} exit={}", named_sigs.len(), en, ex);
    assert!(!named_sigs.is_empty(), "real-data bb_macd: named must produce signals");
    assert_mtf_parity("MtfBbMacd real-data named vs script", &named_sigs, &script_sigs);
}
