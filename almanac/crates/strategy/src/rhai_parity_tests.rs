//! Parity tests: RhaiStrategy ↔ named strategies and CelStrategy.
//!
//! Each test feeds identical bars to both implementations and asserts that
//! signal timestamps and directions match exactly.  TP/SL parity tests also
//! compare `target_price` / `stop_price` within floating-point tolerance.

#![cfg(test)]

use alm_core::signal::{Direction, Signal};
use alm_core::strategy::Strategy;
use serde_json::json;

use crate::factory::build_strategy;
use crate::named::MaCrossover;
use crate::test_utils::{
    assert_parity, bar, dip_in_uptrend_bars, rsi_bars, run, trending_bars,
};

// ── helpers ──────────────────────────────────────────────────────────────────

/// Collect `(timestamp, direction)` from a strategy run.
fn run_sigs(s: &mut dyn Strategy, bars: &[alm_core::Bar]) -> Vec<(i64, Direction)> {
    run(s, bars)
}

/// Collect full `Signal`s from a strategy run.
fn run_full(s: &mut dyn Strategy, bars: &[alm_core::Bar]) -> Vec<Signal> {
    bars.iter().flat_map(|b| s.on_bar(b)).collect()
}

// ── EMA crossover: Rhai ↔ MaCrossover ────────────────────────────────────────

/// Rhai `cross_above` / `cross_below` on EMA(5,20) should produce the exact
/// same signals as the hardcoded `MaCrossover(5,20)` strategy.
#[test]
fn rhai_ma_cross_vs_named() {
    let bars = trending_bars(300);

    let mut named = MaCrossover::new(5, 20);
    let named_sigs = run_sigs(&mut named, &bars);

    let script = r#"
let ema5  = ind.ema(5);
let ema20 = ind.ema(20);

if cross_above(ema5, ema20) { entry = true; }
if cross_below(ema5, ema20) { exit  = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
    let rhai_sigs = run_sigs(rhai.as_mut(), &bars);

    assert!(!named_sigs.is_empty(), "rhai_ma_cross: named must produce signals");
    assert_parity("rhai EMA cross vs MaCrossover", &named_sigs, &rhai_sigs);
}

/// Wider EMA periods (10, 50) — exercises the warmup gap between fast and slow.
#[test]
fn rhai_ma_cross_wide_periods_vs_named() {
    let bars = trending_bars(400);

    let mut named = MaCrossover::new(10, 50);
    let named_sigs = run_sigs(&mut named, &bars);

    let script = r#"
let ema10 = ind.ema(10);
let ema50 = ind.ema(50);

if cross_above(ema10, ema50) { entry = true; }
if cross_below(ema10, ema50) { exit  = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
    let rhai_sigs = run_sigs(rhai.as_mut(), &bars);

    assert_parity("rhai EMA(10,50) vs MaCrossover(10,50)", &named_sigs, &rhai_sigs);
}

// ── RSI threshold: Rhai ↔ CEL ────────────────────────────────────────────────

/// Rhai RSI threshold entry/exit vs CEL equivalent.
///
/// Both use `IndicatorBox(rsi,14)` so warmup and values are identical.
/// Rhai hardcodes strength=1.0, CEL uses default 1.0 — parity helper compares
/// `(timestamp, direction)` only.
#[test]
fn rhai_rsi_vs_cel() {
    let bars = rsi_bars(200);

    // buf_depth=1: only need current value (no prev) → same warmup as CEL rsi(14)
    let script = r#"
let rsi14 = ind.rsi(14, 1);

if rsi14[0] < 30.0 { entry = true; }
if rsi14[0] > 70.0 { exit  = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
    let rhai_sigs = run_sigs(rhai.as_mut(), &bars);

    let mut cel = build_strategy("cel", &json!({
        "entry": "rsi(14) < 30.0",
        "exit":  "rsi(14) > 70.0"
    })).unwrap();
    let cel_sigs = run_sigs(cel.as_mut(), &bars);

    assert!(!rhai_sigs.is_empty(), "rhai_rsi: must produce signals");
    assert_parity("rhai RSI threshold vs CEL", &rhai_sigs, &cel_sigs);
}

// ── TP/SL: Rhai ↔ CelStrategy ────────────────────────────────────────────────

/// ATR-based TP and SL: both Rhai and CEL emit Long signals at the same bars
/// with identical `target_price` and `stop_price` values.
///
/// Rhai:  `tp = close[0] + atr14[0] * 2.0; sl = close[0] - atr14[0] * 1.5`
/// CEL:   `"tp": "entry_price + atr(14) * 2.0", "sl": "entry_price - atr(14) * 1.5"`
///
/// Both use `bar.close` as the entry reference price and ATR(14) from the same
/// IndicatorBox implementation, so the computed levels must be identical.
#[test]
fn rhai_tp_sl_vs_cel() {
    let bars = dip_in_uptrend_bars();

    let rhai_script = r#"
let ema20 = ind.ema(20);
let atr14 = ind.atr(14);

if close[0] > ema20[0] && close[1] <= ema20[1] {
    entry = true;
    tp    = close[0] + atr14[0] * 2.0;
    sl    = close[0] - atr14[0] * 1.5;
}
if close[0] < ema20[0] { exit = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": rhai_script })).unwrap();
    let rhai_full = run_full(rhai.as_mut(), &bars);

    let mut cel = build_strategy("cel", &json!({
        "entry": "close > ema(20) && close[1] <= ema(20)[1]",
        "exit":  "close < ema(20)",
        "tp":    "entry_price + atr(14) * 2.0",
        "sl":    "entry_price - atr(14) * 1.5"
    })).unwrap();
    let cel_full = run_full(cel.as_mut(), &bars);

    // Signal counts and timestamps must match
    let rhai_dirs: Vec<(i64, Direction)> = rhai_full.iter().map(|s| (s.timestamp, s.direction)).collect();
    let cel_dirs:  Vec<(i64, Direction)> = cel_full.iter().map(|s| (s.timestamp, s.direction)).collect();
    assert_parity("rhai TP/SL vs CEL TP/SL (directions)", &rhai_dirs, &cel_dirs);

    // Entry signals: TP and SL levels must match within float tolerance
    let rhai_entries: Vec<&Signal> = rhai_full.iter().filter(|s| s.direction == Direction::Long).collect();
    let cel_entries:  Vec<&Signal> = cel_full.iter().filter(|s| s.direction == Direction::Long).collect();

    for (r, c) in rhai_entries.iter().zip(cel_entries.iter()) {
        let eps = 1e-9;
        let r_tp = r.target_price.expect("rhai entry must have target_price");
        let c_tp = c.target_price.expect("cel entry must have target_price");
        assert!((r_tp - c_tp).abs() < eps,
            "TP mismatch at ts={}: rhai={r_tp}, cel={c_tp}", r.timestamp);

        let r_sl = r.stop_price.expect("rhai entry must have stop_price");
        let c_sl = c.stop_price.expect("cel entry must have stop_price");
        assert!((r_sl - c_sl).abs() < eps,
            "SL mismatch at ts={}: rhai={r_sl}, cel={c_sl}", r.timestamp);
    }
}

/// Fixed-% TP: after a Long, the Rhai signal carries `target_price` at
/// `entry_price * 1.05`.  Verify the field is populated and within 5 % of entry.
#[test]
fn rhai_fixed_pct_tp_field_is_set() {
    let bars = rsi_bars(200);

    let script = r#"
let rsi14 = ind.rsi(14);

if rsi14[0] < 30.0 {
    entry = true;
    tp    = close[0] * 1.05;
    sl    = close[0] * 0.97;
}
if rsi14[0] > 70.0 { exit = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();
    let sigs = run_full(rhai.as_mut(), &bars);

    let entry_sigs: Vec<&Signal> = sigs.iter().filter(|s| s.direction == Direction::Long).collect();
    assert!(!entry_sigs.is_empty(), "rhai_fixed_pct: must produce Long signals");

    for sig in &entry_sigs {
        let price  = sig.price.expect("entry signal must carry price");
        let tp     = sig.target_price.expect("TP must be set");
        let sl     = sig.stop_price.expect("SL must be set");
        assert!((tp - price * 1.05).abs() < 1e-9, "TP should be price * 1.05");
        assert!((sl - price * 0.97).abs() < 1e-9, "SL should be price * 0.97");
    }
}

// ── Reset parity ──────────────────────────────────────────────────────────────

/// `reset()` must produce identical signals when the same bars are replayed.
#[test]
fn rhai_reset_parity() {
    let bars = trending_bars(300);
    let script = r#"
let ema5  = ind.ema(5);
let ema20 = ind.ema(20);

if cross_above(ema5, ema20) { entry = true; }
if cross_below(ema5, ema20) { exit  = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();

    let r1 = run_sigs(rhai.as_mut(), &bars);
    rhai.reset();
    let r2 = run_sigs(rhai.as_mut(), &bars);

    assert!(!r1.is_empty(), "rhai_reset: must produce signals");
    assert_parity("rhai reset: run1 vs run2", &r1, &r2);
}

/// Reset with TP/SL — entry_price is cleared, TP/SL on second run match first.
#[test]
fn rhai_reset_tp_sl_parity() {
    let bars = dip_in_uptrend_bars();
    let script = r#"
let ema20 = ind.ema(20);
let atr14 = ind.atr(14);

if close[0] > ema20[0] && close[1] <= ema20[1] {
    entry = true;
    tp    = close[0] + atr14[0] * 2.0;
    sl    = close[0] - atr14[0] * 1.5;
}
if close[0] < ema20[0] { exit = true; }
"#;
    let mut rhai = build_strategy("rhai", &json!({ "script": script })).unwrap();

    let r1 = run_full(rhai.as_mut(), &bars);
    rhai.reset();
    let r2 = run_full(rhai.as_mut(), &bars);

    // Timestamps and directions must match
    let d1: Vec<(i64, Direction)> = r1.iter().map(|s| (s.timestamp, s.direction)).collect();
    let d2: Vec<(i64, Direction)> = r2.iter().map(|s| (s.timestamp, s.direction)).collect();
    assert_parity("rhai tp/sl reset: directions", &d1, &d2);

    // TP/SL levels must match too
    for (a, b) in r1.iter().zip(r2.iter()) {
        assert_eq!(a.target_price, b.target_price, "TP must be identical after reset");
        assert_eq!(a.stop_price,   b.stop_price,   "SL must be identical after reset");
    }
}
